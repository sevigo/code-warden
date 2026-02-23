package rag

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/sevigo/goframe/embeddings/sparse"
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

func (r *ragService) gatherHyDEContext(ctx context.Context, collection, embedder string, files []internalgithub.ChangedFile) ([][]schema.Document, []int) {
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
		vectorstores.WithNumGenerations(3),
	)

	var finalResults [][]schema.Document
	var finalIndices []int

	for i, file := range files {
		if file.Patch == "" {
			continue
		}

		select {
		case <-ctx.Done():
			r.logger.Warn("HyDE collection cancelled", "error", ctx.Err())
			return finalResults, finalIndices
		default:
		}

		var docs []schema.Document
		var err error

		if isLogicFile(file.Filename) {
			docs, err = retriever.GetRelevantDocuments(ctx, file.Patch)
		} else {
			baseQuery := fmt.Sprintf(hydeBaseQueryPrompt, file.Filename, file.Patch)
			docs, err = rerankingRetriever.GetRelevantDocuments(ctx, baseQuery)
		}

		if err != nil {
			r.logger.Warn("HyDE generation/retrieval failed for file", "file", file.Filename, "error", err)
			continue
		}

		if len(docs) > 0 {
			r.logger.Debug("HyDE docs found", "file", file.Filename, "count", len(docs))
			finalResults = append(finalResults, docs)
			finalIndices = append(finalIndices, i)
		} else {
			r.logger.Debug("no HyDE docs found", "file", file.Filename)
		}
	}

	r.logger.Info("stage completed", "name", "HyDE")
	return finalResults, finalIndices
}

// generateHyDESnippet generates a HyDE snippet.
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
