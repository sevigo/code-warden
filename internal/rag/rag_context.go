package rag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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

// definitionResult holds the result of a symbol definition lookup.
type definitionResult struct {
	symbol  string
	source  string
	content string
	found   bool
}

// symbolResolution tracks the resolution state for recursive symbol lookup.
type symbolResolution struct {
	mu              sync.RWMutex
	resolvedSymbols map[string]struct{} // Symbols we've already looked up
	resolvedDefs    []definitionResult  // Successfully resolved definitions
	totalResolved   int
}

// gatherDefinitionsContext extracts symbols from the changed files and retrieves their definitions
// using a recursive depth-2 resolution strategy.
//
// Resolution algorithm:
//   - Depth 0: Extract symbols from the git diff
//   - Depth 1: Retrieve definitions for diff symbols (concurrent)
//   - Depth 2: Parse definitions with ExtractUsedSymbols and fetch their definitions (concurrent)
//
// This prevents LLM hallucinations by providing complete type dependency context.
func (r *ragService) gatherDefinitionsContext(ctx context.Context, scopedStore storage.ScopedVectorStore, changedFiles []internalgithub.ChangedFile, seenDocs map[string]struct{}, seenDocsMu *sync.RWMutex) string {
	if len(changedFiles) == 0 {
		return ""
	}

	// Depth 0: Extract unique symbols from all changed files
	depth0Symbols := r.extractSymbolsFromChangedFiles(changedFiles)
	if len(depth0Symbols) == 0 {
		r.logger.Info("stage skipped", "name", "SymbolResolution", "reason", "no_symbols_found")
		return ""
	}

	r.logger.Info("stage started", "name", "SymbolResolution", "depth0_symbols", len(depth0Symbols))

	// Initialize resolution state with thread-safe tracking
	resolution := &symbolResolution{
		resolvedSymbols: make(map[string]struct{}),
		resolvedDefs:    make([]definitionResult, 0),
		totalResolved:   0,
	}

	// Mark all depth-0 symbols as seen to avoid duplicate lookups
	for sym := range depth0Symbols {
		resolution.resolvedSymbols[sym] = struct{}{}
	}

	// Depth 1: Fetch definitions for symbols found in the diff
	const maxWorkers = 10
	depth1Defs := r.fetchDefinitionsConcurrently(ctx, scopedStore, depth0Symbols, resolution, maxWorkers)

	// Depth 2: Parse depth-1 definitions to find their dependencies
	depth2Symbols := r.extractSymbolsFromDefinitions(depth1Defs, changedFiles, resolution)

	var depth2Defs []definitionResult
	if len(depth2Symbols) > 0 {
		r.logger.Info("depth 2 symbol resolution", "symbols_found", len(depth2Symbols))
		depth2Defs = r.fetchDefinitionsConcurrently(ctx, scopedStore, depth2Symbols, resolution, maxWorkers)
	}

	// Build the output, filtering against global seenDocs
	var builder strings.Builder
	builder.WriteString("# Resolved Type Definitions\n\n")
	builder.WriteString("The following types are referenced in the diff. Use these definitions to verify field names, types, and method signatures:\n\n")

	// Process all resolved definitions (create new slice to avoid appendAssign linter warning)
	allDefs := make([]definitionResult, 0, len(depth1Defs)+len(depth2Defs))
	allDefs = append(allDefs, depth1Defs...)
	allDefs = append(allDefs, depth2Defs...)
	outputCount := 0
	for _, def := range allDefs {
		if !def.found {
			continue
		}

		// Check global seenDocs to avoid duplicates with other context stages
		docKey := fmt.Sprintf("%s-%s", def.source, def.symbol)
		seenDocsMu.Lock()
		if _, exists := seenDocs[docKey]; exists {
			seenDocsMu.Unlock()
			continue
		}
		seenDocs[docKey] = struct{}{}
		seenDocsMu.Unlock()

		_, _ = fmt.Fprintf(&builder, "## Definition of %s (from %s)\n```\n%s\n```\n\n", def.symbol, def.source, def.content)
		outputCount++
	}

	r.logger.Info("stage completed", "name", "SymbolResolution",
		"depth1_resolved", len(depth1Defs),
		"depth2_symbols", len(depth2Symbols),
		"depth2_resolved", len(depth2Defs),
		"total_output", outputCount)

	if outputCount == 0 {
		return ""
	}

	return builder.String()
}

// extractSymbolsFromChangedFiles extracts unique symbols from git patches.
func (r *ragService) extractSymbolsFromChangedFiles(changedFiles []internalgithub.ChangedFile) map[string]struct{} {
	symbols := make(map[string]struct{})
	for _, f := range changedFiles {
		if f.Patch == "" {
			continue
		}
		extracted := extractSymbolsFromPatch(f.Patch)
		for _, sym := range extracted {
			symbols[sym] = struct{}{}
		}
	}

	// Limit to prevent context explosion
	return limitSymbols(symbols, 20)
}

// fetchDefinitionsConcurrently retrieves definitions for a set of symbols using bounded parallelism.
func (r *ragService) fetchDefinitionsConcurrently(ctx context.Context, scopedStore storage.ScopedVectorStore, symbols map[string]struct{}, resolution *symbolResolution, maxWorkers int) []definitionResult {
	if len(symbols) == 0 {
		return nil
	}

	// Convert symbols to slice for iteration
	symbolList := make([]string, 0, len(symbols))
	for sym := range symbols {
		symbolList = append(symbolList, sym)
	}

	defRetriever := vectorstores.NewDefinitionRetriever(scopedStore)
	results := make([]definitionResult, 0, len(symbolList))
	var resultsMu sync.Mutex

	// Use errgroup with bounded parallelism
	g, gctx := errgroup.WithContext(ctx)
	sem := make(chan struct{}, maxWorkers)

	for _, symbol := range symbolList {
		sym := symbol // Capture loop variable
		g.Go(func() error {
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-gctx.Done():
				return gctx.Err()
			}

			// Double-check we haven't already resolved this symbol
			resolution.mu.RLock()
			_, alreadyResolved := resolution.resolvedSymbols[sym]
			resolution.mu.RUnlock()
			if alreadyResolved {
				// Already processed, skip but don't return error
				return nil
			}

			// Mark as being processed
			resolution.mu.Lock()
			resolution.resolvedSymbols[sym] = struct{}{}
			resolution.mu.Unlock()

			// Fetch definition from vector store
			docs, err := defRetriever.GetDefinition(gctx, sym)
			if err != nil {
				r.logger.Debug("failed to fetch definition", "symbol", sym, "error", err)
				return nil // Don't fail the group for individual lookup failures
			}

			if len(docs) == 0 {
				return nil
			}

			// Take the first match as the definition
			def := docs[0]
			source, _ := def.Metadata["source"].(string)
			content := r.getDocContent(def)

			result := definitionResult{
				symbol:  sym,
				source:  source,
				content: content,
				found:   true,
			}

			resultsMu.Lock()
			results = append(results, result)
			resultsMu.Unlock()

			return nil
		})
	}

	// Wait for all lookups to complete (ignoring errors since we handle them individually)
	_ = g.Wait()

	return results
}

// extractSymbolsFromDefinitions parses resolved definitions to find their dependencies.
// This implements depth-2 symbol resolution by using the parser's ExtractUsedSymbols method.
func (r *ragService) extractSymbolsFromDefinitions(definitions []definitionResult, _ []internalgithub.ChangedFile, resolution *symbolResolution) map[string]struct{} {
	depth2Symbols := make(map[string]struct{})

	for _, def := range definitions {
		if !def.found || def.content == "" {
			continue
		}
		r.extractSymbolsFromDefinition(def, depth2Symbols, resolution)
	}

	return limitSymbols(depth2Symbols, 15)
}

// extractSymbolsFromDefinition extracts symbols from a single definition content.
func (r *ragService) extractSymbolsFromDefinition(def definitionResult, symbols map[string]struct{}, resolution *symbolResolution) {
	parser, err := r.parserRegistry.GetParserForFile(def.source, nil)
	if err != nil {
		// Fallback to regex-based extraction if no parser available
		r.addSymbolsFromRegex(def.content, symbols, resolution)
		return
	}

	// Use parser's ExtractUsedSymbols for more accurate extraction
	usedSymbols := parser.ExtractUsedSymbols(def.content)
	for _, sym := range usedSymbols {
		if !r.isSymbolAlreadyResolved(sym, resolution) {
			symbols[sym] = struct{}{}
		}
	}
}

// addSymbolsFromRegex extracts symbols using regex patterns and adds them to the map.
func (r *ragService) addSymbolsFromRegex(content string, symbols map[string]struct{}, resolution *symbolResolution) {
	extracted := extractSymbolsFromPatch(content)
	for _, sym := range extracted {
		if !r.isSymbolAlreadyResolved(sym, resolution) {
			symbols[sym] = struct{}{}
		}
	}
}

// limitSymbols truncates a symbol map to a maximum size.
func limitSymbols(symbols map[string]struct{}, maxSize int) map[string]struct{} {
	if len(symbols) <= maxSize {
		return symbols
	}
	limited := make(map[string]struct{}, maxSize)
	count := 0
	for sym := range symbols {
		limited[sym] = struct{}{}
		count++
		if count >= maxSize {
			break
		}
	}
	return limited
}

// isSymbolAlreadyResolved checks if a symbol has already been processed.
func (r *ragService) isSymbolAlreadyResolved(symbol string, resolution *symbolResolution) bool {
	resolution.mu.RLock()
	defer resolution.mu.RUnlock()
	_, exists := resolution.resolvedSymbols[symbol]
	return exists
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
