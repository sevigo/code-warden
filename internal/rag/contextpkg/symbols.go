package contextpkg

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/sevigo/goframe/vectorstores"
	"golang.org/x/sync/errgroup"

	internalgithub "github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/storage"
)

var (
	symbolTypeDefRegex      = regexp.MustCompile(`(?m)^\+?\s*type\s+(\w+)\s+(?:struct|interface)`)
	symbolFuncDefRegex      = regexp.MustCompile(`(?m)^\+?\s*func\s+(?:\([^)]+\))?\s*(\w+)`)
	symbolVarDeclRegex      = regexp.MustCompile(`(?m)\bvar\s+\w+\s+(\w+)`)
	symbolTypeAssertRegex   = regexp.MustCompile(`(?m)\b([A-Z]\w*)\{`)
	symbolExportedTypeRegex = regexp.MustCompile(`\b([A-Z]\w+)(?:\.|\{)`)
)

type resolvedDefinition struct {
	Symbol  string
	Source  string
	Content string
}

const maxDepth0Symbols = 25
const maxDepth2Symbols = 15
const maxSymbolWorkers = 10
const maxDefinitionChars = 15000 // Cap definitions to ~15k chars (~4k tokens)

//nolint:unparam // error always nil but signature required for errgroup
func (b *builderImpl) gatherDefinitionsContext(ctx context.Context, scopedStore storage.ScopedVectorStore, changedFiles []internalgithub.ChangedFile) (string, error) {
	if len(changedFiles) == 0 {
		return "", nil
	}

	seenDocs := make(map[string]struct{})
	mu := &sync.RWMutex{}

	symbolList := b.extractDepth0Symbols(changedFiles)
	if len(symbolList) == 0 {
		b.cfg.Logger.Info("stage skipped", "name", "SymbolResolution", "reason", "no_symbols_found")
		return "", nil
	}

	b.cfg.Logger.Debug("extracted symbols from diff", "symbols", symbolList)
	b.cfg.Logger.Info("stage started", "name", "SymbolResolution", "depth0_symbols", len(symbolList))

	defRetriever, err := vectorstores.NewDefinitionRetriever(scopedStore)
	if err != nil {
		b.cfg.Logger.Warn("failed to create definition retriever, skipping symbol resolution", "error", err)
		return "", nil
	}

	seenSymbols := make(map[string]struct{})
	for _, s := range symbolList {
		seenSymbols[s] = struct{}{}
	}

	depth1Defs := b.resolveSymbolsConcurrently(ctx, symbolList, defRetriever, seenDocs, mu)
	b.cfg.Logger.Info("depth-1 resolution complete", "resolved", len(depth1Defs))

	depth2Defs := b.resolveDepth2Symbols(ctx, depth1Defs, seenSymbols, defRetriever, seenDocs, mu)

	return b.formatResolvedDefinitions(depth1Defs, depth2Defs), nil
}

func (b *builderImpl) extractDepth0Symbols(changedFiles []internalgithub.ChangedFile) []string {
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

func (b *builderImpl) resolveDepth2Symbols(
	ctx context.Context,
	depth1Defs []resolvedDefinition,
	seenSymbols map[string]struct{},
	defRetriever *vectorstores.DefinitionRetriever,
	seenDocs map[string]struct{}, mu *sync.RWMutex,
) []resolvedDefinition {
	var candidates []string
	for _, def := range depth1Defs {
		for _, sym := range b.extractTransitiveSymbols(def.Source, def.Content) {
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

	b.cfg.Logger.Info("depth-2 resolution started", "transitive_symbols", len(candidates))
	results := b.resolveSymbolsConcurrently(ctx, candidates, defRetriever, seenDocs, mu)
	b.cfg.Logger.Info("depth-2 resolution complete", "resolved", len(results))
	return results
}

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

func filterRemovedLines(patch string) string {
	lines := strings.Split(patch, "\n")
	var removed []string
	for _, line := range lines {
		if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			removed = append(removed, line[1:]) // Strip the leading '-'
		}
	}
	return strings.Join(removed, "\n")
}

func extractSymbolsFromPatch(patch string) []string {
	symbols := make(map[string]struct{})

	// Extract from both added and removed lines.
	// Deleted or renamed symbols are equally important: the reviewer needs
	// to know what depended on a symbol that was removed or changed.
	sources := []string{filterAddedLines(patch), filterRemovedLines(patch)}

	patterns := []*regexp.Regexp{
		symbolTypeDefRegex,
		symbolFuncDefRegex,
		symbolVarDeclRegex,
		symbolTypeAssertRegex,
		symbolExportedTypeRegex,
	}

	for _, src := range sources {
		if src == "" {
			continue
		}
		for _, re := range patterns {
			matches := re.FindAllStringSubmatch(src, -1)
			for _, match := range matches {
				if len(match) > 1 && len(match[1]) > 1 {
					symbols[match[1]] = struct{}{}
				}
			}
		}
	}

	result := make([]string, 0, len(symbols))
	for sym := range symbols {
		result = append(result, sym)
	}
	return result
}

func (b *builderImpl) formatResolvedDefinitions(depth1Defs, depth2Defs []resolvedDefinition) string {
	allDefs := make([]resolvedDefinition, 0, len(depth1Defs)+len(depth2Defs))
	allDefs = append(allDefs, depth1Defs...)
	allDefs = append(allDefs, depth2Defs...)

	if len(allDefs) == 0 {
		b.cfg.Logger.Info("stage completed", "name", "SymbolResolution", "symbols_resolved", 0)
		return ""
	}

	// Prioritize interface and type definitions over concrete implementations
	sort.Slice(allDefs, func(i, j int) bool {
		iIsInterface := strings.Contains(allDefs[i].Content, "interface{") ||
			strings.Contains(allDefs[i].Content, " interface ")
		jIsInterface := strings.Contains(allDefs[j].Content, "interface{") ||
			strings.Contains(allDefs[j].Content, " interface ")
		if iIsInterface != jIsInterface {
			return iIsInterface
		}

		// Prioritize types in function signatures
		iIsType := strings.Contains(allDefs[i].Content, "type "+allDefs[i].Symbol)
		jIsType := strings.Contains(allDefs[j].Content, "type "+allDefs[j].Symbol)
		if iIsType != jIsType {
			return iIsType
		}

		// Prefer shorter definitions (signatures vs full implementations)
		return len(allDefs[i].Content) < len(allDefs[j].Content)
	})

	var builder strings.Builder
	builder.WriteString("# Resolved Type Definitions\n\n")
	builder.WriteString("The following types are referenced in the diff. Use these definitions to verify field names, types, and method signatures:\n\n")

	totalChars := 0
	capped := false
	for _, def := range allDefs {
		defText := fmt.Sprintf("## Definition of %s (from %s)\n```\n%s\n```\n\n", def.Symbol, def.Source, def.Content)
		if totalChars+len(defText) > maxDefinitionChars {
			capped = true
			break
		}
		builder.WriteString(defText)
		totalChars += len(defText)
	}

	b.cfg.Logger.Info("stage completed", "name", "SymbolResolution",
		"depth1_resolved", len(depth1Defs),
		"depth2_resolved", len(depth2Defs),
		"chars_written", totalChars,
		"capped", capped)

	return builder.String()
}

func (b *builderImpl) resolveSymbolsConcurrently(
	ctx context.Context, symbols []string,
	defRetriever *vectorstores.DefinitionRetriever,
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
			source, content, ok := b.resolveSymbolDefinition(gCtx, sym, defRetriever, seenDocs, mu)
			if ok {
				resultMu.Lock()
				results = append(results, resolvedDefinition{
					Symbol:  sym,
					Source:  source,
					Content: content,
				})
				resultMu.Unlock()
			}
			return nil
		})
	}

	_ = g.Wait()
	return results
}

func (b *builderImpl) extractTransitiveSymbols(source, content string) []string {
	if b.cfg.ParserRegistry == nil {
		return extractSymbolsFromPatch(content)
	}

	ext := filepath.Ext(source)
	if ext == "" {
		return extractSymbolsFromPatch(content)
	}

	parser, err := b.cfg.ParserRegistry.GetParserForExtension(ext)
	if err != nil {
		b.cfg.Logger.Debug("no parser for extension, using regex fallback", "source", source, "ext", ext, "error", err)
		return extractSymbolsFromPatch(content)
	}

	symbols := parser.ExtractUsedSymbols(content)
	if len(symbols) == 0 {
		return extractSymbolsFromPatch(content)
	}
	return symbols
}

func mapKeysToSlice(m map[string]struct{}, maxLen int) []string {
	result := make([]string, 0, len(m))
	for k := range m {
		result = append(result, k)
	}
	sort.Strings(result)
	if len(result) > maxLen {
		result = result[:maxLen]
	}
	return result
}

func (b *builderImpl) resolveSymbolDefinition(ctx context.Context, symbol string, defRetriever *vectorstores.DefinitionRetriever, seenDocs map[string]struct{}, mu *sync.RWMutex) (string, string, bool) {
	docs, err := defRetriever.GetDefinition(ctx, symbol)
	if err != nil {
		b.cfg.Logger.Debug("failed to search for definition", "symbol", symbol, "error", err)
		return "", "", false
	}

	if len(docs) == 0 {
		return "", "", false
	}

	def := docs[0]
	docKey := b.getDocKey(def)

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
	content := b.getDocContent(def)

	return source, content, true
}
