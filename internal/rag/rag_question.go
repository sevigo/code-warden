package rag

import (
	"context"
	"fmt"
	"strings"

	"github.com/sevigo/goframe/chains"
	"github.com/sevigo/goframe/embeddings/sparse"
	"github.com/sevigo/goframe/schema"
	"github.com/sevigo/goframe/vectorstores"
)

type QuestionPromptData struct {
	History  string
	Context  string
	Question string
}

func (r *ragService) AnswerQuestion(ctx context.Context, collectionName, embedderModelName, question string, history []string) (string, error) {
	r.logger.Info("Answering question with RAG context", "collection", collectionName)

	var retriever schema.Retriever
	scopedStore := r.vectorStore.ForRepo(collectionName, embedderModelName)

	sparseQuery, err := sparse.GenerateSparseVector(ctx, question)
	if err != nil {
		r.logger.Warn("failed to generate sparse query", "error", err)
		// Fallback to dense-only
		retriever = vectorstores.ToRetriever(scopedStore, 5)
	} else {
		// Use hybrid search with sparse query
		retriever = vectorstores.ToRetriever(scopedStore, 5, vectorstores.WithSparseQuery(sparseQuery))
	}

	chain := chains.NewRetrievalQA(
		retriever,
		r.generatorLLM,
		chains.WithPromptBuilder(func(q string, docs []schema.Document) (string, error) {
			for _, doc := range docs {
				r.logger.Debug("got a document after similarity search:", "document", doc)
			}
			r.logger.Debug("Retrieved relevant documents for question", "count", len(docs))

			contextString := r.buildContextForPrompt(docs)
			promptData := QuestionPromptData{
				Question: q,
				Context:  contextString,
				History:  strings.Join(history, "\n"),
			}
			return r.promptMgr.Render("question", promptData)
		}),
	)

	answer, err := chain.Call(ctx, question)
	if err != nil {
		return "", fmt.Errorf("QA chain failed: %w", err)
	}

	r.logger.Debug("The final LLM answer is", "answer", answer)
	return answer, nil
}
