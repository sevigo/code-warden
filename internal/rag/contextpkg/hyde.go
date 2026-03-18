package contextpkg

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/sevigo/goframe/embeddings/sparse"
	"github.com/sevigo/goframe/schema"
	"github.com/sevigo/goframe/vectorstores"
	"golang.org/x/sync/errgroup"

	internalgithub "github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/llm"
	indexpkg "github.com/sevigo/code-warden/internal/rag/index"
	"github.com/sevigo/code-warden/internal/storage"
)

type HyDEData struct {
	Patch string
}

const hydeBaseQueryPrompt = "To understand the impact of changes in the file '%s', find relevant code that interacts with or is related to the following diff:\n%s"

type dynamicSparseRetriever struct {
	store   storage.ScopedVectorStore
	numDocs int
	builder *builderImpl
}

func (d dynamicSparseRetriever) GetRelevantDocuments(ctx context.Context, query string) ([]schema.Document, error) {
	semanticQuery := stripPatchNoise(query)
	var searchOpts []vectorstores.Option
	sparseVec, err := sparse.GenerateSparseVector(ctx, query)
	if err != nil {
		d.builder.cfg.Logger.Warn("sparse vector generation failed, falling back to dense", "error", err)
	} else {
		searchOpts = append(searchOpts, vectorstores.WithSparseQuery(sparseVec))
	}
	return d.store.SimilaritySearch(ctx, semanticQuery, d.numDocs, searchOpts...)
}

func (b *builderImpl) gatherHyDEContext(ctx context.Context, collection, embedder string, files []internalgithub.ChangedFile) ([][]schema.Document, []int, error) {
	b.cfg.Logger.Info("stage started", "name", "HyDE")

	scopedStore := b.cfg.VectorStore.ForRepo(collection, embedder)

	baseRetriever := dynamicSparseRetriever{
		store:   scopedStore,
		numDocs: 20,
		builder: b,
	}

	rerankingRetriever := vectorstores.RerankingRetriever{
		Retriever: baseRetriever,
		Reranker:  b.cfg.Reranker,
		TopK:      5,
		CandidateFilter: func(query string, docs []schema.Document) []schema.Document {
			return preFilterBM25(stripPatchNoise(query), docs, 10)
		},
	}

	retriever := vectorstores.NewHyDERetriever(
		rerankingRetriever,
		b.generateHyDESnippet,
		vectorstores.WithNumGenerations(2),
	)

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

		idx := i
		f := file

		g.Go(func() error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			var docs []schema.Document
			var err error

			if indexpkg.IsLogicFile(f.Filename) {
				docs, err = retriever.GetRelevantDocuments(ctx, f.Patch)
			} else {
				baseQuery := fmt.Sprintf(hydeBaseQueryPrompt, f.Filename, f.Patch)
				docs, err = rerankingRetriever.GetRelevantDocuments(ctx, baseQuery)
			}

			if err != nil {
				b.cfg.Logger.Warn("HyDE generation/retrieval failed for file", "file", f.Filename, "error", err)
				return nil
			}

			if len(docs) > 0 {
				b.cfg.Logger.Debug("HyDE docs found", "file", f.Filename, "count", len(docs))
				resultsMu.Lock()
				results = append(results, hydeResult{index: idx, docs: docs})
				resultsMu.Unlock()
			} else {
				b.cfg.Logger.Debug("no HyDE docs found", "file", f.Filename)
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		b.cfg.Logger.Warn("HyDE collection cancelled", "error", err)
		return nil, nil, err
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].index < results[j].index
	})

	finalResults := make([][]schema.Document, len(results))
	finalIndices := make([]int, len(results))
	for i, res := range results {
		finalResults[i] = res.docs
		finalIndices[i] = res.index
	}

	b.cfg.Logger.Info("stage completed", "name", "HyDE", "files_processed", len(results))
	return finalResults, finalIndices, nil
}

func (b *builderImpl) generateHyDESnippet(ctx context.Context, q string) (string, error) {
	patchHash := b.hashPatch(q)

	if b.cfg.HyDECache != nil {
		if cached, ok := b.cfg.HyDECache.Load(patchHash); ok {
			if snippet, valid := cached.(string); valid {
				return snippet, nil
			}
		}
	}

	prompt, err := b.cfg.PromptMgr.Render(llm.HyDEPrompt, HyDEData{Patch: q})
	if err != nil {
		return "", err
	}

	snippet, err := b.cfg.GeneratorLLM.Call(ctx, prompt)
	if err == nil && snippet != "" && b.cfg.HyDECache != nil {
		b.cfg.HyDECache.Store(patchHash, snippet)
	}
	return snippet, err
}

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
			continue
		case strings.HasPrefix(trimmed, "-"):
			continue
		case strings.HasPrefix(trimmed, "+"):
			cleanLines = append(cleanLines, line)
		default:
			if trimmed != "" {
				cleanLines = append(cleanLines, line)
			}
		}
	}
	if len(cleanLines) == 0 {
		return ""
	}
	return strings.Join(cleanLines, "\n")
}

func preFilterBM25(query string, docs []schema.Document, topK int) []schema.Document {
	if len(docs) <= topK {
		return docs
	}

	type scoredDoc struct {
		doc   schema.Document
		score int
	}

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

	sort.SliceStable(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})

	result := make([]schema.Document, topK)
	for i := range topK {
		result[i] = scored[i].doc
	}
	return result
}

func (b *builderImpl) buildHyDEContent(hyde [][]schema.Document, indices []int, files []internalgithub.ChangedFile) string {
	if len(hyde) == 0 {
		return ""
	}

	var builder strings.Builder
	builder.WriteString("# Related Code Snippets\n\nThe following code snippets might be relevant to the changes being reviewed:\n\n")

	seenKeys := make(map[string]struct{})
	for i, hydeDocs := range hyde {
		if i >= len(indices) || indices[i] >= len(files) {
			continue
		}
		filePath := files[indices[i]].Filename
		for _, doc := range hydeDocs {
			key := b.getDocKey(doc)
			if _, exists := seenKeys[key]; exists {
				continue
			}
			seenKeys[key] = struct{}{}
			fmt.Fprintf(&builder, "## Related to: %s\n```\n%s\n```\n\n", filePath, b.getDocContent(doc))
		}
	}

	return builder.String()
}
