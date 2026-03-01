package rag

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"github.com/sevigo/goframe/embeddings/sparse"
	"github.com/sevigo/goframe/schema"
	"github.com/sevigo/goframe/vectorstores"
	"golang.org/x/sync/errgroup"

	internalgithub "github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/llm"
	"github.com/sevigo/code-warden/internal/storage"
)

type HyDEData struct {
	Patch string
}

const hydeBaseQueryPrompt = "To understand the impact of changes in the file '%s', find relevant code that interacts with or is related to the following diff:\n%s"

// dynamicSparseRetriever retrieves documents using sparse vectors if available.
type dynamicSparseRetriever struct {
	store   storage.ScopedVectorStore
	numDocs int
	logger  *slog.Logger
}

func (d dynamicSparseRetriever) GetRelevantDocuments(ctx context.Context, query string) ([]schema.Document, error) {
	semanticQuery := stripPatchNoise(query)
	var searchOpts []vectorstores.Option
	sparseVec, err := sparse.GenerateSparseVector(ctx, query)
	if err != nil {
		d.logger.Warn("sparse vector generation failed, falling back to dense", "error", err)
	} else {
		searchOpts = append(searchOpts, vectorstores.WithSparseQuery(sparseVec))
	}
	return d.store.SimilaritySearch(ctx, semanticQuery, d.numDocs, searchOpts...)
}

// gatherHyDEContext generates hypothetical documents for each changed file
// and retrieves similar code snippets using sparse+dense hybrid search.
func (r *ragService) gatherHyDEContext(ctx context.Context, collection, embedder string, files []internalgithub.ChangedFile) ([][]schema.Document, []int, error) {
	r.logger.Info("stage started", "name", "HyDE")

	scopedStore := r.vectorStore.ForRepo(collection, embedder)

	baseRetriever := dynamicSparseRetriever{
		store:   scopedStore,
		numDocs: 20,
		logger:  r.logger,
	}

	rerankingRetriever := vectorstores.RerankingRetriever{
		Retriever: baseRetriever,
		Reranker:  r.reranker,
		TopK:      5,
		CandidateFilter: func(query string, docs []schema.Document) []schema.Document {
			return preFilterBM25(stripPatchNoise(query), docs, 10)
		},
	}

	retriever := vectorstores.NewHyDERetriever(
		rerankingRetriever,
		r.generateHyDESnippet,
		vectorstores.WithNumGenerations(2),
	)

	// Parallel HyDE generation with concurrency limit
	const maxHyDEConcurrency = 5
	type hydeResult struct {
		index int
		docs  []schema.Document
	}

	var (
		resultsMu sync.Mutex
		results   []hydeResult
	)

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(maxHyDEConcurrency)

	for i, file := range files {
		if file.Patch == "" {
			continue
		}

		// Capture loop variables
		idx := i
		f := file

		g.Go(func() error {
			// Check context cancellation
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			var docs []schema.Document
			var err error

			if isLogicFile(f.Filename) {
				docs, err = retriever.GetRelevantDocuments(ctx, f.Patch)
			} else {
				baseQuery := fmt.Sprintf(hydeBaseQueryPrompt, f.Filename, f.Patch)
				docs, err = rerankingRetriever.GetRelevantDocuments(ctx, baseQuery)
			}

			if err != nil {
				r.logger.Warn("HyDE generation/retrieval failed for file", "file", f.Filename, "error", err)
				return nil // Non-fatal: continue processing other files
			}

			if len(docs) > 0 {
				r.logger.Debug("HyDE docs found", "file", f.Filename, "count", len(docs))
				resultsMu.Lock()
				results = append(results, hydeResult{index: idx, docs: docs})
				resultsMu.Unlock()
			} else {
				r.logger.Debug("no HyDE docs found", "file", f.Filename)
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		r.logger.Warn("HyDE collection cancelled", "error", err)
		return nil, nil, err
	}

	// Sort results by original index to maintain order
	sort.Slice(results, func(i, j int) bool {
		return results[i].index < results[j].index
	})

	// Extract final results
	finalResults := make([][]schema.Document, len(results))
	finalIndices := make([]int, len(results))
	for i, res := range results {
		finalResults[i] = res.docs
		finalIndices[i] = res.index
	}

	r.logger.Info("stage completed", "name", "HyDE", "files_processed", len(results))
	return finalResults, finalIndices, nil
}

// generateHyDESnippet generates a hypothetical code snippet from a diff patch via LLM.
func (r *ragService) generateHyDESnippet(ctx context.Context, q string) (string, error) {
	patchHash := r.hashPatch(q)
	if cached, ok := r.hydeCache.Load(patchHash); ok {
		if snippet, valid := cached.(string); valid {
			return snippet, nil
		}
	}

	prompt, err := r.promptMgr.Render(llm.HyDEPrompt, HyDEData{Patch: q})
	if err != nil {
		return "", err
	}

	snippet, err := r.generatorLLM.Call(ctx, prompt)
	if snippet != "" {
		r.hydeCache.Store(patchHash, snippet)
	}
	return snippet, err
}
