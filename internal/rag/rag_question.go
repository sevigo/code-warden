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

// QuestionPromptData holds data for the Q&A prompt template.
type QuestionPromptData struct {
	History  string
	Context  string
	Question string
}

// AnswerQuestion retrieves relevant documents and generates an answer via LLM.
func (r *ragService) AnswerQuestion(ctx context.Context, collectionName, embedderModelName, question string, history []string) (string, error) {
	r.logger.Info("answering question", "collection", collectionName)

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

	// Use ValidatingRetrievalQA if a fast model is configured.
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

// answerWithValidation uses a fast validator LLM to filter irrelevant context before answering.
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

	// If there's conversation history, we need to incorporate it.
	if len(history) > 0 {
		answer = r.enrichAnswerWithContext(answer, history)
	}

	r.logger.Debug("answer with validation generated", "answer_len", len(answer))
	return answer, nil
}

// answerWithoutValidation uses standard RetrievalQA without context filtering.
func (r *ragService) answerWithoutValidation(ctx context.Context, retriever schema.Retriever, question string, history []string) (string, error) {
	chain, err := chains.NewRetrievalQA(
		retriever,
		r.generatorLLM,
		chains.WithPromptBuilder(func(q string, docs []schema.Document) (string, error) {
			r.logger.Debug("retrieved docs for question", "count", len(docs))
			for i, doc := range docs {
				r.logger.Debug("retrieved doc metadata", "idx", i, "source", doc.Metadata["source"])
			}

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

// enrichAnswerWithContext is a placeholder for future multi-turn conversation support.
// Currently returns the answer unchanged.
func (r *ragService) enrichAnswerWithContext(answer string, _ []string) string {
	// TODO: Implement history incorporation with GoFrame's validation prompts extension.
	// The ValidatingRetrievalQA already considers context relevance.
	// History support can be added by extending GoFrame's validation prompts.
	return answer
}
