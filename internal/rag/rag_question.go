package rag

import (
	"context"
	"fmt"
	"strings"

	"github.com/sevigo/goframe/chains"
	"github.com/sevigo/goframe/embeddings/sparse"
	"github.com/sevigo/goframe/llms"
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

	// Try to use ValidatingRetrievalQA if a fast model is configured for validation
	// This validates the retrieved context before generating an answer
	if r.cfg.AI.FastModel != "" {
		validatorLLM, err := r.getOrCreateLLM(ctx, r.cfg.AI.FastModel)
		if err == nil {
			return r.answerWithValidation(ctx, retriever, validatorLLM, question, history)
		}
		r.logger.Warn("failed to create validator LLM, falling back to non-validating QA", "error", err)
	}

	// Fallback to standard RetrievalQA without validation
	return r.answerWithoutValidation(ctx, retriever, question, history)
}

// answerWithValidation uses ValidatingRetrievalQA to validate context before answering.
// If the retrieved context is not relevant, it generates an answer without context.
func (r *ragService) answerWithValidation(ctx context.Context, retriever schema.Retriever, validatorLLM llms.Model, question string, history []string) (string, error) {
	chain, err := chains.NewValidatingRetrievalQA(
		retriever,
		r.generatorLLM,
		chains.WithValidator(validatorLLM),
		chains.WithLogger(r.logger),
	)
	if err != nil {
		return "", fmt.Errorf("failed to create validating retrieval QA chain: %w", err)
	}

	answer, err := chain.Call(ctx, question)
	if err != nil {
		return "", fmt.Errorf("validating QA chain failed: %w", err)
	}

	// If there's conversation history, we need to incorporate it
	// ValidatingRetrievalQA doesn't support history natively, so we append it
	if len(history) > 0 {
		answer = r.enrichAnswerWithContext(answer, history)
	}

	r.logger.Debug("answer with validation generated", "answer_len", len(answer))
	return answer, nil
}

// answerWithoutValidation uses standard RetrievalQA without context validation.
func (r *ragService) answerWithoutValidation(ctx context.Context, retriever schema.Retriever, question string, history []string) (string, error) {
	chain, err := chains.NewRetrievalQA(
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
	if err != nil {
		return "", fmt.Errorf("failed to create retrieval QA chain: %w", err)
	}

	answer, err := chain.Call(ctx, question)
	if err != nil {
		return "", fmt.Errorf("QA chain failed: %w", err)
	}

	r.logger.Debug("answer without validation generated", "answer_len", len(answer))
	return answer, nil
}

// enrichAnswerWithContext adds conversation history context to the answer when needed.
// TODO: Implement history incorporation with GoFrame's validation prompts extension.
func (r *ragService) enrichAnswerWithContext(answer string, _ []string) string {
	// For now, just return the answer as-is. The ValidatingRetrievalQA already
	// considers the context relevance. History support can be added by extending
	// GoFrame's validation prompts in the future.
	return answer
}
