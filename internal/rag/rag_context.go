package rag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/sevigo/goframe/embeddings/sparse"
	"github.com/sevigo/goframe/schema"
	"github.com/sevigo/goframe/vectorstores"
	"golang.org/x/sync/errgroup"

	internalgithub "github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/storage"
)

func (r *ragService) buildContextForPrompt(docs []schema.Document) string {
	if len(docs) == 0 {
		return ""
	}

	// ── Dedup by key ─────────────────────────────────────────────────────────
	seenDocs := make(map[string]struct{})
	unique := docs[:0]
	for _, doc := range docs {
		key := r.getDocKey(doc)
		if _, exists := seenDocs[key]; exists {
			continue
		}
		seenDocs[key] = struct{}{}
		unique = append(unique, doc)
	}

	// ── Group by source file ──────────────────────────────────────────────────
	type fileEntry struct {
		source string
		docs   []schema.Document
	}
	order := make([]string, 0, len(unique))
	groups := make(map[string]*fileEntry)
	for _, doc := range unique {
		source, _ := doc.Metadata["source"].(string)
		if _, seen := groups[source]; !seen {
			order = append(order, source)
			groups[source] = &fileEntry{source: source}
		}
		groups[source].docs = append(groups[source].docs, doc)
	}

	var contextBuilder strings.Builder
	for _, src := range order {
		entry := groups[src]
		contextBuilder.WriteString("---\n")
		contextBuilder.WriteString(fmt.Sprintf("File: %s\n", src))

		// Print package/identifier from the first doc in the group
		first := entry.docs[0]
		if pkg, ok := first.Metadata["package_name"].(string); ok && pkg != "" {
			contextBuilder.WriteString(fmt.Sprintf("Package: %s\n", pkg))
		}
		if identifier, _ := first.Metadata["identifier"].(string); identifier != "" {
			if parentID, _ := first.Metadata["parent_id"].(string); parentID == "" {
				contextBuilder.WriteString(fmt.Sprintf("Identifier: %s\n", identifier))
			}
		}

		contextBuilder.WriteString("\n")
		contextBuilder.WriteString(mergeChunksForFile(entry.docs, r))
		contextBuilder.WriteString("\n---\n\n")
	}
	return contextBuilder.String()
}

// mergeChunksForFile merges consecutive chunks from the same source file,
// removing overlapping text to produce a single continuous code block.
// This prevents token waste and confusing duplicate snippets for the LLM.
func mergeChunksForFile(docs []schema.Document, r *ragService) string {
	if len(docs) == 1 {
		return r.getDocContent(docs[0])
	}

	var merged strings.Builder
	merged.WriteString(r.getDocContent(docs[0]))

	for i := 1; i < len(docs); i++ {
		prev := merged.String()
		curr := r.getDocContent(docs[i])

		// Try to detect and remove the overlapping prefix
		overlapStart := findOverlapStart(prev, curr)
		if overlapStart > 0 {
			// curr[0:overlapStart] is already present at the end of prev
			merged.WriteString(curr[overlapStart:])
		} else {
			merged.WriteString("\n")
			merged.WriteString(curr)
		}
	}
	return merged.String()
}

// findOverlapStart returns the length of the longest suffix of prev that is
// also a prefix of curr. Checks overlaps of up to 300 characters.
func findOverlapStart(prev, curr string) int {
	const maxOverlap = 300
	overlap := len(prev)
	if overlap > maxOverlap {
		overlap = maxOverlap
		prev = prev[len(prev)-overlap:]
	}
	// Also cap at len(curr) to avoid out-of-bounds on curr[:size]
	if overlap > len(curr) {
		overlap = len(curr)
	}
	// Walk from largest possible overlap down to min 10 chars
	for size := overlap; size >= 10; size-- {
		if strings.HasSuffix(prev, curr[:size]) {
			return size
		}
	}
	return 0
}

// filterAddedLines extracts only the added ('+') lines from a git patch string,
// stripping the leading '+' character. Lines starting with '+++' (file header) are excluded.
// This prevents regex symbol extraction from matching deleted ('-') lines, which could
// produce stale or incorrect symbol names.
func filterAddedLines(patch string) string {
	lines := strings.Split(patch, "\n")
	var added []string
	for _, line := range lines {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			added = append(added, line[1:]) // Strip the leading '+'
		}
	}
	return strings.Join(added, "\n")
}

// extractSymbolsFromPatch extracts potential type/function names from a git patch.
// IMPORTANT: Only added lines ('+') are processed to avoid matching deleted symbols.
// Uses pre-compiled regexes for performance.
func extractSymbolsFromPatch(patch string) []string {
	symbols := make(map[string]struct{})

	// Only extract from added lines — never from deleted lines.
	addedOnly := filterAddedLines(patch)
	if addedOnly == "" {
		return nil
	}

	// Use pre-compiled regexes from package level
	patterns := []*regexp.Regexp{
		symbolTypeDefRegex,
		symbolFuncDefRegex,
		symbolVarDeclRegex,
		symbolTypeAssertRegex,
		symbolExportedTypeRegex,
	}

	for _, re := range patterns {
		matches := re.FindAllStringSubmatch(addedOnly, -1)
		for _, match := range matches {
			if len(match) > 1 && len(match[1]) > 1 {
				symbols[match[1]] = struct{}{}
			}
		}
	}

	result := make([]string, 0, len(symbols))
	for sym := range symbols {
		result = append(result, sym)
	}
	return result
}

// resolvedDefinition holds a resolved symbol definition for building the output.
type resolvedDefinition struct {
	Symbol  string
	Source  string
	Content string
}

// maxDepth0Symbols caps the number of symbols from Depth-0 (diff extraction).
const maxDepth0Symbols = 25

// maxDepth2Symbols caps the number of transitive symbols resolved at Depth-2.
const maxDepth2Symbols = 15

// maxSymbolWorkers limits concurrent DefinitionRetriever lookups at each depth.
const maxSymbolWorkers = 10

// gatherDefinitionsContext extracts symbols from the changed files and retrieves their definitions
// using a two-depth recursive resolution strategy:
//
//	Depth 0: Extract symbols from git diff patches (regex-based).
//	Depth 1: Concurrently retrieve definitions for those symbols from the vector store.
//	Depth 2: Parse retrieved definitions for transitive symbol references (via ExtractUsedSymbols)
//	          and concurrently retrieve those as well.
//
// This ensures the LLM has complete type dependency context (e.g., User → Address → Address.Validate).
func (r *ragService) gatherDefinitionsContext(ctx context.Context, scopedStore storage.ScopedVectorStore, changedFiles []internalgithub.ChangedFile, seenDocs map[string]struct{}, mu *sync.RWMutex) string {
	if len(changedFiles) == 0 {
		return ""
	}

	// ── Depth 0: Extract symbols from diff ──────────────────────────────
	symbolList := r.extractDepth0Symbols(changedFiles)
	if len(symbolList) == 0 {
		r.logger.Info("stage skipped", "name", "SymbolResolution", "reason", "no_symbols_found")
		return ""
	}

	r.logger.Info("stage started", "name", "SymbolResolution", "depth0_symbols", len(symbolList))

	seenSymbols := make(map[string]struct{})
	for _, s := range symbolList {
		seenSymbols[s] = struct{}{}
	}

	// ── Depth 1: Concurrently resolve Depth-0 symbols ───────────────────
	depth1Defs := r.resolveSymbolsConcurrently(ctx, symbolList, scopedStore, seenDocs, mu)
	r.logger.Info("depth-1 resolution complete", "resolved", len(depth1Defs))

	// ── Depth 2: Resolve transitive symbols found inside Depth-1 definitions ──
	depth2Defs := r.resolveDepth2Symbols(ctx, depth1Defs, seenSymbols, scopedStore, seenDocs, mu)

	// ── Format output ───────────────────────────────────────────────────
	return r.formatResolvedDefinitions(depth1Defs, depth2Defs)
}

// extractDepth0Symbols extracts unique symbols from all changed file patches.
func (r *ragService) extractDepth0Symbols(changedFiles []internalgithub.ChangedFile) []string {
	depth0Symbols := make(map[string]struct{})
	for _, f := range changedFiles {
		if f.Patch == "" {
			continue
		}
		for _, sym := range extractSymbolsFromPatch(f.Patch) {
			depth0Symbols[sym] = struct{}{}
		}
	}
	return mapKeysToSlice(depth0Symbols, maxDepth0Symbols)
}

// resolveDepth2Symbols extracts transitive symbols from Depth-1 definitions,
// deduplicates them, and resolves the new ones concurrently.
func (r *ragService) resolveDepth2Symbols(
	ctx context.Context,
	depth1Defs []resolvedDefinition,
	seenSymbols map[string]struct{},
	scopedStore storage.ScopedVectorStore,
	seenDocs map[string]struct{}, mu *sync.RWMutex,
) []resolvedDefinition {
	var candidates []string
	for _, def := range depth1Defs {
		for _, sym := range r.extractTransitiveSymbols(def.Source, def.Content) {
			if _, seen := seenSymbols[sym]; !seen {
				seenSymbols[sym] = struct{}{}
				candidates = append(candidates, sym)
				if len(candidates) >= maxDepth2Symbols {
					break
				}
			}
		}
		if len(candidates) >= maxDepth2Symbols {
			break
		}
	}

	if len(candidates) == 0 {
		return nil
	}

	r.logger.Info("depth-2 resolution started", "transitive_symbols", len(candidates))
	results := r.resolveSymbolsConcurrently(ctx, candidates, scopedStore, seenDocs, mu)
	r.logger.Info("depth-2 resolution complete", "resolved", len(results))
	return results
}

// formatResolvedDefinitions builds the markdown output from all resolved definitions.
func (r *ragService) formatResolvedDefinitions(depth1Defs, depth2Defs []resolvedDefinition) string {
	allDefs := make([]resolvedDefinition, 0, len(depth1Defs)+len(depth2Defs))
	allDefs = append(allDefs, depth1Defs...)
	allDefs = append(allDefs, depth2Defs...)

	if len(allDefs) == 0 {
		r.logger.Info("stage completed", "name", "SymbolResolution", "symbols_resolved", 0)
		return ""
	}

	var builder strings.Builder
	builder.WriteString("# Resolved Type Definitions\n\n")
	builder.WriteString("The following types are referenced in the diff. Use these definitions to verify field names, types, and method signatures:\n\n")

	for _, def := range allDefs {
		_, _ = fmt.Fprintf(&builder, "## Definition of %s (from %s)\n```\n%s\n```\n\n", def.Symbol, def.Source, def.Content)
	}

	r.logger.Info("stage completed", "name", "SymbolResolution",
		"depth1_resolved", len(depth1Defs),
		"depth2_resolved", len(depth2Defs),
	)

	return builder.String()
}

// resolveSymbolsConcurrently resolves a list of symbols in parallel using errgroup.
// Returns the successfully resolved definitions.
func (r *ragService) resolveSymbolsConcurrently(
	ctx context.Context, symbols []string,
	scopedStore storage.ScopedVectorStore,
	seenDocs map[string]struct{}, mu *sync.RWMutex,
) []resolvedDefinition {
	var (
		resultMu sync.Mutex
		results  []resolvedDefinition
	)

	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(maxSymbolWorkers)

	for _, sym := range symbols {
		g.Go(func() error {
			source, content, ok := r.resolveSymbolDefinition(gCtx, sym, scopedStore, seenDocs, mu)
			if ok {
				resultMu.Lock()
				results = append(results, resolvedDefinition{
					Symbol:  sym,
					Source:  source,
					Content: content,
				})
				resultMu.Unlock()
			}
			return nil // Non-fatal: continue even if individual lookups fail
		})
	}

	_ = g.Wait() // Errors are handled per-symbol above
	return results
}

// extractTransitiveSymbols parses a resolved definition's content to find
// additional symbols referenced within it (e.g., field types, method parameters).
// Uses the language-aware parser from the registry when available, falling back
// to the regex-based extractor.
func (r *ragService) extractTransitiveSymbols(source, content string) []string {
	if r.parserRegistry == nil {
		return extractSymbolsFromPatch(content) // Fallback to regex
	}

	ext := filepath.Ext(source)
	if ext == "" {
		return extractSymbolsFromPatch(content)
	}

	parser, err := r.parserRegistry.GetParserForExtension(ext)
	if err != nil {
		r.logger.Debug("no parser for extension, using regex fallback",
			"source", source, "ext", ext, "error", err,
		)
		return extractSymbolsFromPatch(content)
	}

	symbols := parser.ExtractUsedSymbols(content)
	if len(symbols) == 0 {
		return extractSymbolsFromPatch(content)
	}
	return symbols
}

// mapKeysToSlice converts a map's keys to a slice, capping at maxLen.
func mapKeysToSlice(m map[string]struct{}, maxLen int) []string {
	result := make([]string, 0, min(len(m), maxLen))
	for k := range m {
		result = append(result, k)
		if len(result) >= maxLen {
			break
		}
	}
	return result
}

func (r *ragService) resolveSymbolDefinition(ctx context.Context, symbol string, scopedStore storage.ScopedVectorStore, seenDocs map[string]struct{}, mu *sync.RWMutex) (string, string, bool) {
	defRetriever := vectorstores.NewDefinitionRetriever(scopedStore)
	docs, err := defRetriever.GetDefinition(ctx, symbol)

	if err != nil {
		r.logger.Debug("failed to search for definition", "symbol", symbol, "error", err)
		return "", "", false
	}

	if len(docs) == 0 {
		return "", "", false
	}

	// Take the first match as the definition
	def := docs[0]
	docKey := r.getDocKey(def)

	mu.RLock()
	_, exists := seenDocs[docKey]
	mu.RUnlock()
	if exists {
		return "", "", false
	}

	mu.Lock()
	seenDocs[docKey] = struct{}{}
	mu.Unlock()

	source, _ := def.Metadata["source"].(string)
	content := r.getDocContent(def)

	return source, content, true
}

// buildRelevantContext performs similarity searches using file diffs to find related
// code snippets from the repository. These results provide context to help the LLM
// better understand the scope and impact of the changes.
func (r *ragService) buildRelevantContext(ctx context.Context, collectionName, embedderModelName, repoPath string, changedFiles []internalgithub.ChangedFile, prDescription string) (string, string) {
	if len(changedFiles) == 0 {
		return "", ""
	}

	const defaultMaxContextFiles = 20
	if len(changedFiles) > defaultMaxContextFiles {
		r.logger.Warn("truncating context files", "total", len(changedFiles), "limit", defaultMaxContextFiles)
		changedFiles = changedFiles[:defaultMaxContextFiles]
	}

	scopedStore := r.vectorStore.ForRepo(collectionName, embedderModelName)

	archContext, definitionsContext, impactDocs, descDocs, hydeResults, indices := r.buildContextConcurrently(
		ctx, collectionName, embedderModelName, repoPath, prDescription, changedFiles, scopedStore)

	allDocs := mergeAndDedup(append(impactDocs, descDocs...), r.getDocKey)

	var impactContext, descriptionContext string
	if len(allDocs) > 0 {
		var seenDocs sync.Map
		impactContext, descriptionContext = r.splitAndFormatDocs(allDocs, descDocs, prDescription, &seenDocs)
	}

	fullContext := r.assembleContext(archContext, impactContext, descriptionContext, definitionsContext, hydeResults, indices, changedFiles)
	return fullContext, definitionsContext
}

func (r *ragService) buildContextConcurrently(
	ctx context.Context, collectionName, embedderModelName, repoPath, prDescription string,
	changedFiles []internalgithub.ChangedFile, scopedStore storage.ScopedVectorStore,
) (archContext, definitionsContext string, impactDocs, descDocs []schema.Document, hydeResults [][]schema.Document, indices []int) {
	var wg sync.WaitGroup
	var impactMu, descMu sync.Mutex

	wg.Add(1)
	go func() {
		defer wg.Done()
		archContext = r.gatherArchContextSafe(ctx, scopedStore, changedFiles)
	}()

	if r.cfg.AI.EnableHyDE {
		wg.Add(1)
		go func() {
			defer wg.Done()
			hydeResults, indices = r.gatherHyDEContext(ctx, collectionName, embedderModelName, changedFiles)
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		docs := r.gatherImpactDocs(ctx, scopedStore, repoPath, changedFiles)
		impactMu.Lock()
		impactDocs = append(impactDocs, docs...)
		impactMu.Unlock()
	}()

	if prDescription != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			docs := r.gatherDescriptionDocs(ctx, collectionName, embedderModelName, prDescription)
			descMu.Lock()
			descDocs = append(descDocs, docs...)
			descMu.Unlock()
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		seenDocs := make(map[string]struct{})
		var mu sync.RWMutex
		definitionsContext = r.gatherDefinitionsContext(ctx, scopedStore, changedFiles, seenDocs, &mu)
	}()

	wg.Wait()
	return
}

func (r *ragService) gatherArchContextSafe(ctx context.Context, store storage.ScopedVectorStore, files []internalgithub.ChangedFile) string {
	r.logger.Info("stage started", "name", "ArchitecturalContext")
	ac := r.getArchContext(ctx, store, files)
	r.logger.Info("stage completed", "name", "ArchitecturalContext")
	return ac
}

// mergeAndDedup merges document slices and deduplicates them by a key function.
// The output order is deterministic: docs are sorted by key after dedup.
func mergeAndDedup(docs []schema.Document, keyFn func(schema.Document) string) []schema.Document {
	seen := make(map[string]schema.Document, len(docs))
	for _, d := range docs {
		key := keyFn(d)
		if _, exists := seen[key]; !exists {
			seen[key] = d
		}
	}
	unique := make([]schema.Document, 0, len(seen))
	for _, d := range seen {
		unique = append(unique, d)
	}
	sort.Slice(unique, func(i, j int) bool {
		si, _ := unique[i].Metadata["source"].(string)
		sj, _ := unique[j].Metadata["source"].(string)
		return si < sj
	})
	return unique
}

// splitAndFormatDocs splits merged docs back into impact/description buckets and formats them.
func (r *ragService) splitAndFormatDocs(
	allDocs []schema.Document,
	descDocs []schema.Document,
	prDescription string,
	seen *sync.Map,
) (impactContext, descriptionContext string) {
	descKeys := make(map[string]schema.Document, len(descDocs))
	for _, d := range descDocs {
		source, _ := d.Metadata["source"].(string)
		descKeys[source] = d
	}

	validDescSources := r.filterValidDescriptionDocs(descKeys, seen, prDescription)
	return r.formatSplitDocs(allDocs, descKeys, validDescSources, seen, prDescription)
}

func (r *ragService) filterValidDescriptionDocs(descKeys map[string]schema.Document, seen *sync.Map, prDescription string) map[string]bool {
	var toValidate []schema.Document
	var snippets []string
	for source, d := range descKeys {
		if _, loaded := seen.Load(source); !loaded {
			toValidate = append(toValidate, d)
			snippets = append(snippets, r.getDocContent(d))
		}
	}

	relevanceMap := make(map[int]bool, len(toValidate))
	if len(snippets) > 0 && prDescription != "" {
		validatorLLM, err := r.getOrCreateLLM(r.cfg.AI.FastModel)
		if err == nil {
			v := newSnippetValidator(validatorLLM, r.promptMgr)
			relevanceMap = v.validateBatch(context.Background(), snippets, prDescription)
		} else {
			for i := range snippets {
				relevanceMap[i] = true
			}
		}
	}

	validSources := make(map[string]bool, len(toValidate))
	for i, doc := range toValidate {
		if relevanceMap[i] {
			source, _ := doc.Metadata["source"].(string)
			validSources[source] = true
		}
	}
	return validSources
}

func (r *ragService) formatSplitDocs(
	allDocs []schema.Document,
	descKeys map[string]schema.Document,
	validDescSources map[string]bool,
	seen *sync.Map,
	prDescription string,
) (string, string) {
	var impactBuilder, descBuilder strings.Builder
	for _, doc := range allDocs {
		source, _ := doc.Metadata["source"].(string)

		if _, isDesc := descKeys[source]; isDesc && prDescription != "" {
			if !validDescSources[source] {
				continue
			}
		}

		if _, loaded := seen.LoadOrStore(source, struct{}{}); loaded {
			continue
		}

		content := r.getDocContent(doc)
		if _, isDesc := descKeys[source]; isDesc && prDescription != "" {
			descBuilder.WriteString(fmt.Sprintf("File: %s\n```\n%s\n```\n\n", source, content))
		} else {
			fmt.Fprintf(&impactBuilder, "**%s**:\n```\n%s\n```\n\n", source, content)
		}
	}

	var descCtx string
	if descBuilder.Len() > 0 {
		descCtx = "# Related to PR Description\n\n" + descBuilder.String()
	}
	return impactBuilder.String(), descCtx
}

// gatherDescriptionDocs uses MultiQuery retrieval to find documents related to the PR description.
// Returns raw []schema.Document for deterministic dedup in the caller.
func (r *ragService) gatherDescriptionDocs(ctx context.Context, collection, embedder, description string) []schema.Document {
	r.logger.Info("stage started", "name", "DescriptionContext")

	scopedStore := r.vectorStore.ForRepo(collection, embedder)

	queryLLM, err := r.getOrCreateLLM(r.cfg.AI.FastModel)
	if err != nil {
		queryLLM = r.generatorLLM
	}

	retriever := vectorstores.MultiQueryRetriever{
		Store:        scopedStore,
		LLM:          queryLLM,
		NumDocuments: 10,
		Count:        3,
		SparseGenFunc: func(ctx context.Context, queries []string) ([]*schema.SparseVector, error) {
			var vecs []*schema.SparseVector
			for _, q := range queries {
				v, err := sparse.GenerateSparseVector(ctx, q)
				if err != nil {
					r.logger.Warn("Failed to generate sparse vector for MultiQuery fallback", "query", q, "error", err)
					return nil, err
				}
				vecs = append(vecs, v)
			}
			return vecs, nil
		},
	}

	allDocs, err := retriever.GetRelevantDocuments(ctx, description)
	if err != nil {
		r.logger.Warn("multi-query retrieval failed", "error", err)
		return nil
	}

	r.logger.Info("stage completed", "name", "DescriptionContext", "retrieved", len(allDocs))
	return allDocs
}

// gatherImpactDocs returns raw impact docs without formatting or shared seenDocs.
// Replaces gatherImpactContext: dedup now happens after wg.Wait() in the caller.
func (r *ragService) gatherImpactDocs(ctx context.Context, store storage.ScopedVectorStore, repoPath string, files []internalgithub.ChangedFile) []schema.Document {
	r.logger.Info("stage started", "name", "ImpactAnalysis")
	docs := r.getImpactDocs(ctx, store, repoPath, files)
	r.logger.Info("stage completed", "name", "ImpactAnalysis", "docs", len(docs))
	return docs
}

// assembleContext assembles the final prompt context from all gathered sections,
// applying a token budget to prevent context window overflow (Issue #3).
// Priority order (high → low): definitions → description → impact → arch → hyde.
func (r *ragService) assembleContext(
	arch, impact, description, definitions string,
	hyde [][]schema.Document, indices []int,
	files []internalgithub.ChangedFile,
) string {
	const tokenBudget = 6000
	packer := newTokenContextPacker(tokenBudget)

	// Priority 1: Definitions (type context is most critical for accuracy)
	if definitions != "" {
		packer.add("Definitions", definitions)
	}
	// Priority 2: Description-related snippets
	if description != "" {
		packer.add("Description", description)
	}
	// Priority 3: Impact analysis
	if impact != "" {
		packer.add("Impact", fmt.Sprintf("# Potential Impacted Callers & Usages\n\nThe following code snippets may be affected by the changes in modified symbols:\n\n%s", impact))
	}
	// Priority 4: Architectural context
	if arch != "" {
		packer.add("Arch", fmt.Sprintf("# Architectural Context\n\nThe following describes the purpose of the affected modules:\n\n%s", arch))
	}
	// Priority 5: HyDE snippets (may be redundant if impact/description already covered)
	if len(hyde) > 0 {
		var hydeBuilder strings.Builder
		hydeBuilder.WriteString("# Related Code Snippets\n\nThe following code snippets might be relevant to the changes being reviewed:\n\n")
		hydeSeenKeys := make(map[string]struct{})
		for i, docs := range hyde {
			if i >= len(indices) {
				continue
			}
			originalIdx := indices[i]
			if originalIdx >= len(files) {
				continue
			}
			filePath := files[originalIdx].Filename
			for _, doc := range docs {
				key := r.getDocKey(doc)
				if _, exists := hydeSeenKeys[key]; exists {
					continue
				}
				hydeSeenKeys[key] = struct{}{}
				hydeBuilder.WriteString(fmt.Sprintf("## Related to: %s\n", filePath))
				hydeBuilder.WriteString("```\n")
				hydeBuilder.WriteString(r.getDocContent(doc))
				hydeBuilder.WriteString("\n```\n\n")
			}
		}
		packer.add("HyDE", hydeBuilder.String())
	}

	result := packer.build()

	r.logger.Info("relevant context built",
		"changed_files", len(files),
		"arch_len", len(arch),
		"impact_len", len(impact),
		"definitions_len", len(definitions),
		"hyde_results_count", len(hyde),
		"packed_len", len(result),
	)

	return result
}

// tokenContextSection represents a named section in the context packer.
type tokenContextSection struct {
	name    string
	content string
}

// tokenContextPacker packs context sections into a token budget, truncating
// lower-priority sections first. It uses a simple character-based estimate
// (1 token ≈ 3 characters) for speed — no extra LLM calls needed.
type tokenContextPacker struct {
	budget   int
	sections []tokenContextSection
}

func newTokenContextPacker(tokenBudget int) *tokenContextPacker {
	return &tokenContextPacker{budget: tokenBudget}
}

// add appends a named section. Sections are packed in the order they are added
// (first added = highest priority).
func (p *tokenContextPacker) add(name, content string) {
	if content != "" {
		p.sections = append(p.sections, tokenContextSection{name: name, content: content})
	}
}

// estimateTokens returns a fast character-based token estimate (1 token ≈ 3 chars).
func estimateTokens(s string) int { return len(s) / 3 }

// build assembles sections into a single string, truncating lower-priority
// sections when the total would exceed the token budget.
func (p *tokenContextPacker) build() string {
	var out strings.Builder
	remaining := p.budget

	for _, sec := range p.sections {
		tokens := estimateTokens(sec.content)

		switch {
		case tokens <= remaining:
			out.WriteString(sec.content)
			out.WriteString("\n---\n\n")
			remaining -= tokens

		case remaining > 50: // Only truncate if there's meaningful space left
			// Truncate to remaining budget
			maxChars := remaining * 3
			truncated := sec.content[:maxChars]
			// Try to cut at a newline boundary
			if idx := strings.LastIndex(truncated, "\n"); idx > maxChars/2 {
				truncated = truncated[:idx]
			}
			out.WriteString(truncated)
			out.WriteString(fmt.Sprintf("\n[%s context truncated due to length]\n\n---\n\n", sec.name))
			remaining = 0

		default: // Budget exhausted
			out.WriteString(fmt.Sprintf("[%s context omitted — token budget exhausted]\n\n---\n\n", sec.name))
		}
	}

	return out.String()
}

func (r *ragService) processRelatedSnippet(doc schema.Document, originalFile internalgithub.ChangedFile, rank int, seenDocs map[string]struct{}, seenMu *sync.RWMutex, topFiles []string, builder *strings.Builder) []string {
	source, _ := doc.Metadata["source"].(string)
	if source == "" || r.isArchDocument(doc) {
		return topFiles
	}

	parentID, ok := doc.Metadata["parent_id"].(string)
	if !ok {
		parentID = ""
	}
	docKey := parentID
	if docKey == "" {
		docKey = source
	}

	seenMu.RLock()
	_, exists := seenDocs[docKey]
	seenMu.RUnlock()

	if !exists {
		if len(topFiles) < 3 {
			topFiles = append(topFiles, source)
		}
		// Swap snippet with full parent text if available
		content := doc.PageContent
		if parentText, ok := doc.Metadata["full_parent_text"].(string); ok && parentText != "" {
			content = parentText
		}
		fmt.Fprintf(builder, "**%s** (relevant to %s):\n```\n%s\n```\n\n",
			source, originalFile.Filename, content)

		seenMu.Lock()
		seenDocs[docKey] = struct{}{}
		seenMu.Unlock()
	}

	// Fallback: even if we've seen it, if it's top result for another file, it's worth noting in debug logs
	if rank == 0 && len(topFiles) < 3 {
		alreadyLogged := false
		for _, f := range topFiles {
			if f == source {
				alreadyLogged = true
				break
			}
		}
		if !alreadyLogged {
			topFiles = append(topFiles, source)
		}
	}
	return topFiles
}

func (r *ragService) getArchContext(ctx context.Context, scopedStore storage.ScopedVectorStore, files []internalgithub.ChangedFile) string {
	filePaths := make([]string, len(files))
	for i, f := range files {
		filePaths[i] = f.Filename
	}
	archContext, err := r.GetArchContextForPaths(ctx, scopedStore, filePaths)
	if err != nil {
		r.logger.Warn("failed to get architectural context", "error", err)
		return ""
	}
	if archContext != "" {
		r.logger.Debug("retrieved architectural context", "folders_count", len(filePaths))
	}
	return archContext
}

func (r *ragService) isArchDocument(doc schema.Document) bool {
	ct, ok := doc.Metadata["chunk_type"].(string)
	return ok && ct == "arch"
}

// stripPatchNoise removes git metadata and deleted lines, preserving additions and context for semantic search.
func stripPatchNoise(query string) string {
	if query == "" {
		return ""
	}
	lines := strings.Split(query, "\n")
	var cleanLines []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "diff --git"):
			continue
		case strings.HasPrefix(trimmed, "index "):
			continue
		case strings.HasPrefix(trimmed, "new file mode"):
			continue
		case strings.HasPrefix(trimmed, "deleted file mode"):
			continue
		case strings.HasPrefix(trimmed, "--- "), strings.HasPrefix(trimmed, "+++ "), strings.HasPrefix(trimmed, "@@"):
			continue // Strip diff headers
		case strings.HasPrefix(trimmed, "-"):
			continue // Skip deleted lines
		case strings.HasPrefix(trimmed, "+"):
			// Preserve additions with their + prefix so the LLM recognizes them as new code
			cleanLines = append(cleanLines, line)
		default:
			if trimmed != "" {
				cleanLines = append(cleanLines, line) // Preserve context and HyDE preamble
			}
		}
	}
	if len(cleanLines) == 0 {
		return ""
	}
	return strings.Join(cleanLines, "\n")
}

// preFilterBM25 performs a simple keyword-overlap based ranking to trim results
// before sending them to the expensive reranker.
func preFilterBM25(query string, docs []schema.Document, topK int) []schema.Document {
	if len(docs) <= topK {
		return docs
	}

	type scoredDoc struct {
		doc   schema.Document
		score int
	}

	// Simple keyword overlap score
	queryTerms := strings.Fields(strings.ToLower(query))
	filteredTerms := make([]string, 0, len(queryTerms))
	for _, t := range queryTerms {
		if len(t) >= 3 {
			filteredTerms = append(filteredTerms, t)
		}
	}

	if len(filteredTerms) == 0 {
		return docs
	}

	scored := make([]scoredDoc, len(docs))
	for i, doc := range docs {
		score := 0
		content := strings.ToLower(doc.PageContent)
		for _, term := range filteredTerms {
			if strings.Contains(content, term) {
				score++
			}
		}
		scored[i] = scoredDoc{doc: doc, score: score}
	}

	// Sort by overlap score
	sort.SliceStable(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})

	result := make([]schema.Document, topK)
	for i := range topK {
		result[i] = scored[i].doc
	}
	return result
}

func (r *ragService) getDocKey(doc schema.Document) string {
	source, _ := doc.Metadata["source"].(string)
	identifier, _ := doc.Metadata["identifier"].(string)
	parentID, ok := doc.Metadata["parent_id"].(string)
	if ok && parentID != "" {
		return parentID
	}
	if identifier != "" && source != "" {
		return fmt.Sprintf("%s-%s", source, identifier)
	}
	if source != "" {
		return source
	}
	h := sha256.Sum256([]byte(doc.PageContent))
	return hex.EncodeToString(h[:])
}

func (r *ragService) getDocContent(doc schema.Document) string {
	if parentText, ok := doc.Metadata["full_parent_text"].(string); ok && parentText != "" {
		return parentText
	}
	if parentID, ok := doc.Metadata["parent_id"].(string); ok && parentID != "" {
		r.logger.Debug("parent_id present but full_parent_text missing", "parent_id", parentID, "source", doc.Metadata["source"])
	}
	return doc.PageContent
}
