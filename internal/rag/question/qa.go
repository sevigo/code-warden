package question

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/sevigo/goframe/chains"
	"github.com/sevigo/goframe/embeddings/sparse"
	"github.com/sevigo/goframe/llms"
	"github.com/sevigo/goframe/schema"
	"github.com/sevigo/goframe/vectorstores"

	"github.com/sevigo/code-warden/internal/llm"
	"github.com/sevigo/code-warden/internal/storage"
)

// PromptData holds data for the Q&A prompt template.
type PromptData struct {
	History  string
	Context  string
	Question string
}

// Config holds dependencies for the QAService.
type Config struct {
	VectorStore   storage.VectorStore
	GeneratorLLM  llms.Model
	ValidatorLLM  llms.Model // Optional fast model for filtering irrelevant context
	PromptMgr     *llm.PromptManager
	Logger        *slog.Logger
	ContextFormat func([]schema.Document) string
}

// QAService orchestrates question answering over repositories.
type QAService struct {
	cfg Config
}

// NewService creates a new [QAService] instance.
func NewService(cfg Config) *QAService {
	return &QAService{cfg: cfg}
}

// AnswerQuestion retrieves relevant documents and generates an answer via LLM.
func (s *QAService) AnswerQuestion(ctx context.Context, collectionName, embedderModelName, question string, history []string) (string, error) {
	s.cfg.Logger.Info("answering question", "collection", collectionName)

	var retriever schema.Retriever
	scopedStore := s.cfg.VectorStore.ForRepo(collectionName, embedderModelName)

	sparseQuery, err := sparse.GenerateSparseVector(ctx, question)
	if err != nil {
		s.cfg.Logger.Warn("failed to generate sparse query", "error", err)
		// Fallback to dense-only
		retriever = vectorstores.ToRetriever(scopedStore, 5)
	} else {
		// Use hybrid search with sparse query
		retriever = vectorstores.ToRetriever(scopedStore, 5, vectorstores.WithSparseQuery(sparseQuery))
	}

	// Use ValidatingRetrievalQA if a validator LLM is configured.
	if s.cfg.ValidatorLLM != nil {
		return s.AnswerWithValidation(ctx, retriever, question, history)
	}

	// Fallback to standard RetrievalQA without validation
	return s.AnswerWithoutValidation(ctx, retriever, question, history)
}

// AnswerWithValidation uses a fast validator LLM to filter irrelevant context before answering.
func (s *QAService) AnswerWithValidation(ctx context.Context, retriever schema.Retriever, question string, _ []string) (string, error) {
	chain, err := chains.NewValidatingRetrievalQA(
		retriever,
		s.cfg.GeneratorLLM,
		chains.WithValidator(s.cfg.ValidatorLLM),
		chains.WithLogger(s.cfg.Logger),
	)
	if err != nil {
		return "", fmt.Errorf("failed to create validating retrieval QA chain: %w", err)
	}

	answer, err := chain.Call(ctx, question)
	if err != nil {
		return "", fmt.Errorf("validating QA chain failed: %w", err)
	}

	s.cfg.Logger.Debug("answer with validation generated", "answer_len", len(answer))
	return answer, nil
}

// AnswerWithoutValidation uses standard RetrievalQA without context filtering.
func (s *QAService) AnswerWithoutValidation(ctx context.Context, retriever schema.Retriever, question string, history []string) (string, error) {
	chain, err := chains.NewRetrievalQA(
		retriever,
		s.cfg.GeneratorLLM,
		chains.WithPromptBuilder(func(q string, docs []schema.Document) (string, error) {
			s.cfg.Logger.Debug("retrieved docs for question", "count", len(docs))
			for i, doc := range docs {
				s.cfg.Logger.Debug("retrieved doc metadata", "idx", i, "source", doc.Metadata["source"])
			}

			contextString := s.cfg.ContextFormat(docs)
			promptData := PromptData{
				Question: q,
				Context:  contextString,
				History:  strings.Join(history, "\n"),
			}
			return s.cfg.PromptMgr.Render("question", promptData)
		}),
	)
	if err != nil {
		return "", fmt.Errorf("failed to create retrieval QA chain: %w", err)
	}

	answer, err := chain.Call(ctx, question)
	if err != nil {
		return "", fmt.Errorf("QA chain failed: %w", err)
	}

	s.cfg.Logger.Debug("answer without validation generated", "answer_len", len(answer))
	return answer, nil
}
