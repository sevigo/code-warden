package rag

import (
	"context"
	"fmt"
	"sync"

	"github.com/sevigo/goframe/embeddings/sparse"
	"github.com/sevigo/goframe/llms"
	"github.com/sevigo/goframe/schema"
	"github.com/sevigo/goframe/vectorstores"

	internalgithub "github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/llm"
	"github.com/sevigo/code-warden/internal/storage"
)

type HyDEData struct {
	Patch string
}

const hydeBaseQueryPrompt = "To understand the impact of changes in the file '%s', find relevant code that interacts with or is related to the following diff:\n%s"

func (r *ragService) gatherHyDEContext(ctx context.Context, collection, embedder string, files []internalgithub.ChangedFile) ([][]schema.Document, []int) {
	r.logger.Info("stage started", "name", "HyDE")

	workChan := make(chan struct {
		originalIdx int
		query       string
	}, len(files)*2)
	resultsChan := make(chan struct {
		idx  int
		docs []schema.Document
	}, len(files)*2)

	var searchWg sync.WaitGroup
	var genWg sync.WaitGroup

	// 1. Start Search Workers
	scopedStore := r.vectorStore.ForRepo(collection, embedder)
	for range 3 {
		searchWg.Add(1)
		go func() {
			defer searchWg.Done()
			for work := range workChan {
				docs := r.performSingleHyDEJob(ctx, scopedStore, work.query)
				if len(docs) > 0 {
					resultsChan <- struct {
						idx  int
						docs []schema.Document
					}{work.originalIdx, docs}
				}
			}
		}()
	}

	// 2. Start Generator
	go r.runHyDEGenerator(ctx, files, &genWg, workChan)

	// 3. Collector (waits for workers)
	go func() {
		searchWg.Wait()
		close(resultsChan)
	}()

	return r.collectHyDEResults(ctx, resultsChan)
}

func (r *ragService) runHyDEGenerator(ctx context.Context, files []internalgithub.ChangedFile, wg *sync.WaitGroup, workChan chan<- struct {
	originalIdx int
	query       string
}) {
	defer close(workChan)

	maxConcurrency := r.cfg.AI.HyDEConcurrency
	if maxConcurrency <= 0 {
		maxConcurrency = 5
	}
	hydeSem := make(chan struct{}, maxConcurrency)
	for i, file := range files {
		if file.Patch == "" {
			continue
		}

		// Queue Base Query IMMEDIATELY
		baseQuery := fmt.Sprintf(hydeBaseQueryPrompt, file.Filename, file.Patch)
		workChan <- struct {
			originalIdx int
			query       string
		}{originalIdx: i, query: baseQuery}

		// Queue HyDE Snippet Generation (Async)
		if isLogicFile(file.Filename) {
			wg.Add(1)
			go func(idx int, f internalgithub.ChangedFile) {
				defer wg.Done()
				snippet := r.generateSingleHyDESnippet(ctx, f, hydeSem)
				if snippet != "" {
					workChan <- struct {
						originalIdx int
						query       string
					}{originalIdx: idx, query: snippet}
				}
			}(i, file)
		}
	}
	wg.Wait()
}

func (r *ragService) collectHyDEResults(ctx context.Context, resultsChan <-chan struct {
	idx  int
	docs []schema.Document
}) ([][]schema.Document, []int) {
	var finalResults [][]schema.Document
	var finalIndices []int

	for {
		select {
		case res, ok := <-resultsChan:
			if !ok {
				r.logger.Info("HyDE collection completed", "queries_processed", len(finalResults))
				return finalResults, finalIndices
			}
			finalResults = append(finalResults, res.docs)
			finalIndices = append(finalIndices, res.idx)
		case <-ctx.Done():
			r.logger.Warn("HyDE collection cancelled", "error", ctx.Err())
			return finalResults, finalIndices
		}
	}
}

func (r *ragService) performSingleHyDEJob(ctx context.Context, scopedStore storage.ScopedVectorStore, rawQuery string) []schema.Document {
	// 1. Clean the Query (Bottleneck #5: Strip diff noise)
	semanticQuery := stripPatchNoise(rawQuery)

	var searchOpts []vectorstores.Option
	// Use un-stripped query for Sparse Vector to capture exact identifiers in diff
	sparseVec, err := sparse.GenerateSparseVector(ctx, rawQuery)
	if err != nil {
		r.logger.Warn("sparse vector generation failed for HyDE query, falling back to dense", "query", semanticQuery, "error", err)
	} else {
		searchOpts = append(searchOpts, vectorstores.WithSparseQuery(sparseVec))
	}

	// Recall
	baseDocs, err := scopedStore.SimilaritySearch(ctx, semanticQuery, 20, searchOpts...)
	if err != nil {
		r.logger.Warn("base hybrid search failed", "error", err)
		return nil
	}

	// 2. Pre-filter by Keyword Score (Bottleneck #2: N+1 Reranking)
	preFilteredDocs := preFilterBM25(semanticQuery, baseDocs, 10)

	// Precision (Rerank)
	scoredDocs, err := r.reranker.Rerank(ctx, semanticQuery, preFilteredDocs)
	if err != nil {
		r.logger.Warn("reranking failed, falling back", "error", err)
		return r.fallbackDocs(preFilteredDocs, 5)
	}

	return r.formatScoredDocs(scoredDocs, 5)
}

func (r *ragService) fallbackDocs(docs []schema.Document, limit int) []schema.Document {
	if len(docs) > limit {
		return docs[:limit]
	}
	return docs
}

func (r *ragService) formatScoredDocs(scoredDocs []schema.ScoredDocument, limit int) []schema.Document {
	count := len(scoredDocs)
	if count > limit {
		count = limit
	}
	docs := make([]schema.Document, count)
	for j := range count {
		docs[j] = scoredDocs[j].Document
		if docs[j].Metadata == nil {
			docs[j].Metadata = make(map[string]any)
		}
		docs[j].Metadata["score"] = scoredDocs[j].Score
		docs[j].Metadata["rerank_reason"] = scoredDocs[j].Reason
	}
	return docs
}

func (r *ragService) generateSingleHyDESnippet(ctx context.Context, file internalgithub.ChangedFile, sem chan struct{}) string {
	patchHash := r.hashPatch(file.Patch)
	if cached, ok := r.hydeCache.Load(patchHash); ok {
		if snippet, valid := cached.(string); valid {
			return snippet
		}
	}

	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-ctx.Done():
		return ""
	}

	prompt, err := r.promptMgr.Render(llm.HyDEPrompt, HyDEData{Patch: file.Patch})
	if err != nil {
		r.logger.Error("failed to render HyDE prompt", "error", err, "file", file.Filename)
		return ""
	}

	snippet, _ := llms.GenerateFromSinglePrompt(ctx, r.generatorLLM, prompt)
	if snippet != "" {
		r.hydeCache.Store(patchHash, snippet)
	} else {
		r.logger.Error("HyDE generation returned empty result", "file", file.Filename, "patchHash", patchHash)
	}
	return snippet
}
