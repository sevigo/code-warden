package rag

import (
	"context"

	"github.com/sevigo/goframe/schema"
	"github.com/sevigo/goframe/vectorstores"

	internalgithub "github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/llm"
)

type HyDEData struct {
	Patch string
}

const hydeBaseQueryPrompt = "To understand the impact of changes in the file '%s', find relevant code that interacts with or is related to the following diff:\n%s"

func (r *ragService) gatherHyDEContext(ctx context.Context, collection, embedder string, files []internalgithub.ChangedFile) ([][]schema.Document, []int) {
	r.logger.Info("stage started", "name", "HyDE")

	scopedStore := r.vectorStore.ForRepo(collection, embedder)

	retriever := vectorstores.NewHyDERetriever(
		vectorstores.ToRetriever(scopedStore, 5),
		func(ctx context.Context, q string) (string, error) {
			prompt, err := r.promptMgr.Render(llm.HyDEPrompt, HyDEData{Patch: q})
			if err != nil {
				return "", err
			}
			return r.generatorLLM.Call(ctx, prompt)
		},
		vectorstores.WithNumGenerations(3),
	)

	var finalResults [][]schema.Document
	var finalIndices []int

	for i, file := range files {
		if file.Patch == "" {
			continue
		}
		if !isLogicFile(file.Filename) {
			continue
		}

		select {
		case <-ctx.Done():
			r.logger.Warn("HyDE collection cancelled", "error", ctx.Err())
			return finalResults, finalIndices
		default:
		}

		docs, err := retriever.GetRelevantDocuments(ctx, file.Patch)
		if err != nil {
			r.logger.Warn("HyDE generation failed for file", "file", file.Filename, "error", err)
			continue
		}

		if len(docs) > 0 {
			finalResults = append(finalResults, docs)
			finalIndices = append(finalIndices, i)
		}
	}

	r.logger.Info("stage completed", "name", "HyDE")
	return finalResults, finalIndices
}
