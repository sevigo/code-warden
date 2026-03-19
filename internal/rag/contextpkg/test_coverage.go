package contextpkg

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/sevigo/goframe/embeddings/sparse"
	"github.com/sevigo/goframe/schema"
	"github.com/sevigo/goframe/vectorstores"
	"golang.org/x/sync/errgroup"

	internalgithub "github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/storage"
)

const maxCoverageChunks = 10

// gatherTestCoverageContext finds test files related to changed production code
// and retrieves test chunks that test the changed symbols.
func (b *builderImpl) gatherTestCoverageContext(
	ctx context.Context,
	scopedStore storage.ScopedVectorStore,
	changedFiles []internalgithub.ChangedFile,
	definitionsContext string,
) ([]schema.Document, error) {
	b.cfg.Logger.Info("stage started", "name", "TestCoverage")

	// Extract symbols from definitions context
	symbols := extractSymbolsFromDefinitions(definitionsContext)
	if len(symbols) == 0 {
		b.cfg.Logger.Info("stage completed", "name", "TestCoverage", "reason", "no_symbols_found")
		return nil, nil
	}

	// Get source files from changed files
	sourceFiles := make(map[string]bool)
	for _, f := range changedFiles {
		if f.Patch != "" && !isTestFilePath(f.Filename) {
			sourceFiles[f.Filename] = true
		}
	}

	if len(sourceFiles) == 0 {
		b.cfg.Logger.Info("stage completed", "name", "TestCoverage", "reason", "no_source_files")
		return nil, nil
	}

	var (
		resultsMu sync.Mutex
		results   []schema.Document
	)

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(5)

	// Search for test chunks that test the symbols we found
	for symbol := range symbols {
		sym := symbol
		g.Go(func() error {
			docs, err := b.searchTestChunksForSymbol(ctx, scopedStore, sym, sourceFiles)
			if err != nil {
				b.cfg.Logger.Debug("failed to search test chunks", "symbol", sym, "error", err)
				return nil
			}

			if len(docs) > 0 {
				resultsMu.Lock()
				results = append(results, docs...)
				resultsMu.Unlock()
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	// Deduplicate by document key
	seen := make(map[string]bool)
	deduped := make([]schema.Document, 0, len(results))
	for _, doc := range results {
		key := b.getDocKey(doc)
		if !seen[key] {
			seen[key] = true
			deduped = append(deduped, doc)
		}
	}

	// Limit to max chunks
	if len(deduped) > maxCoverageChunks {
		deduped = deduped[:maxCoverageChunks]
	}

	b.cfg.Logger.Info("stage completed", "name", "TestCoverage",
		"symbols_searched", len(symbols),
		"test_chunks_found", len(deduped))

	return deduped, nil
}

// searchTestChunksForSymbol searches for test chunks that test a specific symbol.
//
//nolint:gocognit
func (b *builderImpl) searchTestChunksForSymbol(
	ctx context.Context,
	scopedStore storage.ScopedVectorStore,
	symbol string,
	sourceFiles map[string]bool,
) ([]schema.Document, error) {
	// Search for test chunks
	opts := []vectorstores.Option{
		vectorstores.WithFilter("is_test", true),
	}
	if b.cfg.AIConfig.RetrievalScoreThreshold > 0 {
		opts = append(opts, vectorstores.WithScoreThreshold(b.cfg.AIConfig.RetrievalScoreThreshold))
	}
	if sparseVec, sparseErr := sparse.GenerateSparseVector(ctx, symbol); sparseErr == nil {
		opts = append(opts, vectorstores.WithSparseQuery(sparseVec))
	} else {
		b.cfg.Logger.Debug("sparse vector generation failed for test coverage, using dense only", "symbol", symbol, "error", sparseErr)
	}
	docs, err := scopedStore.SimilaritySearch(ctx, symbol, 5, opts...)
	if err != nil {
		return nil, err
	}

	// Filter to only include tests for files we're reviewing
	var relevant []schema.Document
	for _, doc := range docs {
		if testedSymbols, ok := doc.Metadata["tested_symbols"].([]string); ok {
			for _, ts := range testedSymbols {
				if ts == symbol || strings.HasSuffix(ts, "."+symbol) {
					relevant = append(relevant, doc)
					break
				}
			}
			continue
		}
		if testedSymbols, ok := doc.Metadata["tested_symbols"].([]any); ok {
			for _, ts := range testedSymbols {
				if tsStr, ok := ts.(string); ok {
					if tsStr == symbol || strings.HasSuffix(tsStr, "."+symbol) {
						relevant = append(relevant, doc)
						break
					}
				}
			}
			continue
		}
		// Also source file match
		if sourceFile, ok := doc.Metadata["source_file"].(string); ok {
			if sourceFiles[sourceFile] {
				relevant = append(relevant, doc)
			}
		}
	}

	// Deduplicate
	seen := make(map[string]bool)
	var unique []schema.Document
	for _, doc := range relevant {
		key := fmt.Sprintf("%s:%d", doc.Metadata["source"], doc.Metadata["line"])
		if !seen[key] {
			seen[key] = true
			unique = append(unique, doc)
		}
	}

	return unique, nil
}

// extractSymbolsFromDefinitions extracts symbol names from the definitions context.
//
//nolint:gocognit
func extractSymbolsFromDefinitions(definitionsContext string) map[string]bool {
	symbols := make(map[string]bool)

	if definitionsContext == "" {
		return symbols
	}

	// Pattern: "## Definition of SymbolName"
	lines := strings.Split(definitionsContext, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "## Definition of ") {
			// Extract symbol name
			parts := strings.SplitN(line, " ", 5)
			if len(parts) >= 4 {
				symbol := strings.TrimSuffix(parts[3], " (from")
				symbols[symbol] = true
			}
		}
	}

	// Also extract from diff if present (patterns like "func Foo", "type Foo")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		// Match function definitions
		if strings.HasPrefix(line, "func ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				// Handle method receivers: func (r *Receiver) Method
				if strings.HasPrefix(parts[1], "(") && len(parts) >= 4 {
					symbols[parts[3]] = true
				} else {
					symbols[parts[1]] = true
				}
			}
		}
		// Match type definitions
		if strings.HasPrefix(line, "type ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				symbols[parts[1]] = true
			}
		}
	}

	return symbols
}

// isTestFilePath checks if a path is a test file.
func isTestFilePath(path string) bool {
	return strings.HasSuffix(path, "_test.go") ||
		strings.HasSuffix(path, ".test.ts") ||
		strings.HasSuffix(path, ".test.tsx") ||
		strings.HasSuffix(path, ".spec.ts") ||
		strings.HasSuffix(path, ".spec.tsx") ||
		strings.HasSuffix(path, ".test.js") ||
		strings.HasSuffix(path, ".spec.js") ||
		strings.HasSuffix(path, "_test.py")
}

// formatTestCoverageContext formats test coverage documents into context.
func (b *builderImpl) formatTestCoverageContext(docs []schema.Document) string {
	if len(docs) == 0 {
		return ""
	}

	var builder strings.Builder
	builder.WriteString("# Test Coverage\n\n")
	builder.WriteString("The following tests are relevant to the code being reviewed. They may help identify edge cases:\n\n")

	for _, doc := range docs {
		source, _ := doc.Metadata["source"].(string)
		testedSymbols, _ := doc.Metadata["tested_symbols"].([]string)

		fmt.Fprintf(&builder, "## From %s\n", source)
		if len(testedSymbols) > 0 {
			fmt.Fprintf(&builder, "Tests: %s\n", strings.Join(testedSymbols, ", "))
		}
		builder.WriteString("```\n")
		builder.WriteString(b.getDocContent(doc))
		builder.WriteString("\n```\n\n")
	}

	return builder.String()
}
