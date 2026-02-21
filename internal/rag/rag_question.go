package rag

import (
	"context"
	"fmt"
	"strings"

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

	var relevantDocs []schema.Document
	sparseQuery, err := sparse.GenerateSparseVector(ctx, question)
	if err != nil {
		r.logger.Warn("failed to generate sparse query", "error", err)
		// Fallback to dense-only
		relevantDocs, err = r.vectorStore.SearchCollection(ctx, collectionName, embedderModelName, question, 5)
	} else {
		// Use hybrid search with sparse query
		scopedStore := r.vectorStore.ForRepo(collectionName, embedderModelName)
		relevantDocs, err = scopedStore.SimilaritySearch(ctx, question, 5, vectorstores.WithSparseQuery(sparseQuery))
	}

	for _, doc := range relevantDocs {
		r.logger.Debug("got a document after similarity search:", "document", doc)
	}
	if err != nil {
		return "", fmt.Errorf("failed to perform similarity search: %w", err)
	}
	r.logger.Debug("Retrieved relevant documents for question", "count", len(relevantDocs))

	contextString := r.buildContextForPrompt(relevantDocs)

	promptData := QuestionPromptData{
		Question: question,
		Context:  contextString,
		History:  strings.Join(history, "\n"),
	}
	prompt, err := r.promptMgr.Render("question", promptData)
	if err != nil {
		return "", fmt.Errorf("could not render question prompt: %w", err)
	}

	answer, err := r.generatorLLM.Call(ctx, prompt)
	if err != nil {
		return "", fmt.Errorf("LLM call failed for question: %w", err)
	}

	r.logger.Debug("The final LLM answer is", "answer", answer)

	return answer, nil
}
