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
	var contextBuilder strings.Builder
	seenDocs := make(map[string]struct{})

	for _, doc := range docs {
		docKey := r.getDocKey(doc)
		if _, exists := seenDocs[docKey]; exists {
			continue
		}
		seenDocs[docKey] = struct{}{}

		source, _ := doc.Metadata["source"].(string)
		contextBuilder.WriteString("---\n")
		contextBuilder.WriteString(fmt.Sprintf("File: %s\n", source))

		if pkg, ok := doc.Metadata["package_name"].(string); ok && pkg != "" {
			contextBuilder.WriteString(fmt.Sprintf("Package: %s\n", pkg))
		}

		if identifier, _ := doc.Metadata["identifier"].(string); identifier != "" {
			if parentID, _ := doc.Metadata["parent_id"].(string); parentID == "" {
				contextBuilder.WriteString(fmt.Sprintf("Identifier: %s\n", identifier))
			}
		}

		contextBuilder.WriteString("\n")
		contextBuilder.WriteString(r.getDocContent(doc))
		contextBuilder.WriteString("\n---\n\n")
	}
	return contextBuilder.String()
}

// extractSymbolsFromPatch extracts potential type/function names from a git patch.
// Uses pre-compiled regexes for performance. This is a simple regex-based extraction
// until ExtractUsedSymbols is available in GoFrame.
func extractSymbolsFromPatch(patch string) []string {
	symbols := make(map[string]struct{})

	// Use pre-compiled regexes from package level
	patterns := []*regexp.Regexp{
		symbolTypeDefRegex,
		symbolFuncDefRegex,
		symbolVarDeclRegex,
		symbolTypeAssertRegex,
		symbolExportedTypeRegex,
	}

	for _, re := range patterns {
		matches := re.FindAllStringSubmatch(patch, -1)
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
// better understand the scope and impact of the changes. Duplicate entries are avoided.
// It also fetches architectural summaries for the affected directories.
// Returns the combined context string and the definitions context separately.
func (r *ragService) buildRelevantContext(ctx context.Context, collectionName, embedderModelName, repoPath string, changedFiles []internalgithub.ChangedFile, prDescription string) (string, string) {
	if len(changedFiles) == 0 {
		return "", ""
	}

	// Bound the number of files processed to prevent OOM/DoS
	const defaultMaxContextFiles = 20
	if len(changedFiles) > defaultMaxContextFiles {
		r.logger.Warn("truncating context files", "total", len(changedFiles), "limit", defaultMaxContextFiles)
		changedFiles = changedFiles[:defaultMaxContextFiles]
	}

	scopedStore := r.vectorStore.ForRepo(collectionName, embedderModelName)
	var seenDocsMu sync.RWMutex
	seenDocs := make(map[string]struct{})

	// Run context gathering in parallel for lower latency
	var wg sync.WaitGroup
	var archContext, impactContext, descriptionContext string
	var hydeResults [][]schema.Document
	var indices []int

	// 1. Architectural Context
	wg.Add(1)
	go func() {
		defer wg.Done()
		archContext = r.gatherArchContext(ctx, scopedStore, changedFiles)
	}()

	// 2. HyDE Snippets
	if r.cfg.AI.EnableHyDE {
		wg.Add(1)
		go func() {
			defer wg.Done()
			hydeResults, indices = r.gatherHyDEContext(ctx, collectionName, embedderModelName, changedFiles)
		}()
	} else {
		r.logger.Info("stage skipped", "name", "HyDE", "reason", "disabled_in_config")
	}

	// 3. Impact Context
	wg.Add(1)
	go func() {
		defer wg.Done()
		impactContext = r.gatherImpactContext(ctx, scopedStore, repoPath, changedFiles, seenDocs, &seenDocsMu)
	}()

	// 4. Description Context (MultiQuery)
	if prDescription != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			descriptionContext = r.gatherDescriptionContext(ctx, collectionName, embedderModelName, prDescription, seenDocs, &seenDocsMu)
		}()
	}

	// 5. Symbol Resolution (Definitions)
	var definitionsContext string
	wg.Add(1)
	go func() {
		defer wg.Done()
		definitionsContext = r.gatherDefinitionsContext(ctx, scopedStore, changedFiles, seenDocs, &seenDocsMu)
	}()

	wg.Wait()

	// Assemble and return the combined context from all stages.
	fullContext := r.assembleContext(archContext, impactContext, descriptionContext, definitionsContext, hydeResults, indices, changedFiles, seenDocs, &seenDocsMu)
	return fullContext, definitionsContext
}

// gatherDescriptionContext uses MultiQuery retrieval to find code related to the PR description.
func (r *ragService) gatherDescriptionContext(ctx context.Context, collection, embedder, description string, seen map[string]struct{}, mu *sync.RWMutex) string {
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
		return ""
	}

	// Deduplicate by parent-aware key
	uniqueDocs := make(map[string]schema.Document)
	for _, d := range allDocs {
		uniqueDocs[r.getDocKey(d)] = d
	}

	var builder strings.Builder
	if len(uniqueDocs) > 0 {
		builder.WriteString("# Related to PR Description\n\n")
		builder.WriteString(r.validateAndFormatSnippets(ctx, description, uniqueDocs, seen, mu))
	}

	r.logger.Info("stage completed", "name", "DescriptionContext", "unique_snippets", len(uniqueDocs))
	return builder.String()
}

// validateSnippetRelevance uses a fast LLM to check if a retrieved snippet
// is actually relevant to the given context. Fails open (returns true) if
// the validator model is unavailable or returns an error.
// Uses goframe's LLMChain pattern with structured JSON output parsing.
func (r *ragService) validateSnippetRelevance(ctx context.Context, snippet, prContext string) bool {
	validatorLLM, err := r.getOrCreateLLM(r.cfg.AI.FastModel)
	if err != nil {
		return true // Fail open: if no validator available, include the snippet
	}

	validator := newSnippetValidator(validatorLLM, r.promptMgr)
	return validator.validate(ctx, snippet, prContext)
}

func (r *ragService) gatherArchContext(ctx context.Context, store storage.ScopedVectorStore, files []internalgithub.ChangedFile) string {
	r.logger.Info("stage started", "name", "ArchitecturalContext")
	ac := r.getArchContext(ctx, store, files)
	r.logger.Info("stage completed", "name", "ArchitecturalContext")
	return ac
}

func (r *ragService) gatherImpactContext(ctx context.Context, store storage.ScopedVectorStore, repoPath string, files []internalgithub.ChangedFile, seen map[string]struct{}, mu *sync.RWMutex) string {
	r.logger.Info("stage started", "name", "ImpactAnalysis")
	ic := r.getImpactContext(ctx, store, repoPath, files, seen, mu)
	r.logger.Info("stage completed", "name", "ImpactAnalysis")
	return ic
}

func (r *ragService) assembleContext(arch, impact, description, definitions string, hyde [][]schema.Document, indices []int, files []internalgithub.ChangedFile, seen map[string]struct{}, mu *sync.RWMutex) string {
	var contextBuilder strings.Builder

	if arch != "" {
		contextBuilder.WriteString("# Architectural Context\n\n")
		contextBuilder.WriteString("The following describes the purpose of the affected modules:\n\n")
		contextBuilder.WriteString(arch)
		contextBuilder.WriteString("\n---\n\n")
	}

	if description != "" {
		contextBuilder.WriteString(description) // Already formatted in gatherDescriptionContext
		contextBuilder.WriteString("\n---\n\n")
	}

	if definitions != "" {
		contextBuilder.WriteString(definitions) // Already formatted in gatherDefinitionsContext
		contextBuilder.WriteString("\n---\n\n")
	}

	if impact != "" {
		r.logger.Info("impact analysis identified potential ripple effects", "context_length", len(impact))
		contextBuilder.WriteString("# Potential Impacted Callers & Usages\n\n")
		contextBuilder.WriteString("The following code snippets may be affected by the changes in modified symbols:\n\n")
		contextBuilder.WriteString(impact)
		contextBuilder.WriteString("\n---\n\n")
	}

	if len(hyde) > 0 {
		contextBuilder.WriteString("# Related Code Snippets\n\n")
		contextBuilder.WriteString("The following code snippets might be relevant to the changes being reviewed:\n\n")

		for i, docs := range hyde {
			if i >= len(indices) { // Safety check
				continue
			}
			originalIdx := indices[i]
			if originalIdx >= len(files) { // Safety check
				continue
			}
			filePath := files[originalIdx].Filename
			for _, doc := range docs {
				docKey := r.getDocKey(doc)
				mu.Lock()
				if _, exists := seen[docKey]; exists {
					mu.Unlock()
					continue
				}
				seen[docKey] = struct{}{}
				mu.Unlock()

				contextBuilder.WriteString(fmt.Sprintf("## Related to: %s\n", filePath))
				contextBuilder.WriteString("```\n")
				contextBuilder.WriteString(r.getDocContent(doc))
				contextBuilder.WriteString("\n```\n\n")
			}
		}
	}

	r.logger.Info("relevant context built",
		"changed_files", len(files),
		"arch_len", len(arch),
		"impact_len", len(impact),
		"definitions_len", len(definitions),
		"hyde_results_count", len(hyde),
	)

	return contextBuilder.String()
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

func (r *ragService) validateAndFormatSnippets(ctx context.Context, description string, uniqueDocs map[string]schema.Document, seen map[string]struct{}, mu *sync.RWMutex) string {
	const maxConcurrentValidations = 10
	sem := make(chan struct{}, maxConcurrentValidations)
	var validated []string
	var validMu sync.Mutex
	var valWg sync.WaitGroup

	for _, d := range uniqueDocs {
		valWg.Add(1)
		go func(doc schema.Document) {
			defer valWg.Done()

			// Concurrency control
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			content := r.getDocContent(doc)
			docKey := r.getDocKey(doc)

			if ctx.Err() != nil {
				return
			}

			// First check: fast path with read lock
			mu.RLock()
			_, exists := seen[docKey]
			mu.RUnlock()
			if exists {
				return
			}

			// Perform expensive validation only if potentially new
			if !r.validateSnippetRelevance(ctx, content, description) {
				return
			}

			// Second check: verify still absent under write lock before adding
			mu.Lock()
			if _, exists := seen[docKey]; !exists {
				seen[docKey] = struct{}{}
				source, _ := doc.Metadata["source"].(string)
				snip := fmt.Sprintf("File: %s\n```\n%s\n```\n\n", source, content)
				validMu.Lock()
				validated = append(validated, snip)
				validMu.Unlock()
			}
			mu.Unlock()
		}(d)
	}
	valWg.Wait()

	var builder strings.Builder
	for _, v := range validated {
		builder.WriteString(v)
	}
	return builder.String()
}
