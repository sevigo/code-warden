package question

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/sevigo/goframe/chains"
	"github.com/sevigo/goframe/embeddings/sparse"
	"github.com/sevigo/goframe/llms"
	"github.com/sevigo/goframe/schema"
	"github.com/sevigo/goframe/vectorstores"

	"github.com/sevigo/code-warden/internal/llm"
	"github.com/sevigo/code-warden/internal/storage"
)

const (
	archResultLimit = 2
	similarityLimit = 5
)

var pathPattern = regexp.MustCompile(`(?:^|\s|["'` + "`" + `])([\w/.-]+/[\w/.-]+)(?:$|\s|["'` + "`" + `])`)

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
	ValidatorLLM  llms.Model
	PromptMgr     *llm.PromptManager
	Logger        *slog.Logger
	ContextFormat func([]schema.Document) string
}

// QAService orchestrates question answering over repositories.
type QAService struct {
	cfg Config
}

// NewService creates a new QAService instance.
func NewService(cfg Config) *QAService {
	return &QAService{cfg: cfg}
}

type hybridRetriever struct {
	store     storage.ScopedVectorStore
	archDocs  []schema.Document
	sparse    *schema.SparseVector
	baseLimit int
}

func (r *hybridRetriever) GetRelevantDocuments(ctx context.Context, query string) ([]schema.Document, error) {
	var docs []schema.Document
	var err error

	if r.sparse != nil {
		docs, err = r.store.SimilaritySearch(ctx, query, r.baseLimit, vectorstores.WithSparseQuery(r.sparse))
	} else {
		docs, err = r.store.SimilaritySearch(ctx, query, r.baseLimit)
	}
	if err != nil {
		return nil, err
	}

	result := make([]schema.Document, 0, len(r.archDocs)+len(docs))
	result = append(result, r.archDocs...)
	result = append(result, docs...)
	return deduplicateDocs(result), nil
}

func deduplicateDocs(docs []schema.Document) []schema.Document {
	seen := make(map[string]bool)
	var result []schema.Document
	for _, doc := range docs {
		source, _ := doc.Metadata["source"].(string)
		var startLine int
		switch v := doc.Metadata["start_line"].(type) {
		case int:
			startLine = v
		case float64:
			startLine = int(v)
		case int64:
			startLine = int(v)
		}
		key := fmt.Sprintf("%s:%d", source, startLine)
		if !seen[key] {
			seen[key] = true
			result = append(result, doc)
		}
	}
	return result
}

func (s *QAService) AnswerQuestion(ctx context.Context, collectionName, embedderModelName, question string, history []string) (string, error) {
	s.cfg.Logger.Info("answering question", "collection", collectionName)

	scopedStore := s.cfg.VectorStore.ForRepo(collectionName, embedderModelName)

	relevantDocs := s.retrieveRelevantDocs(ctx, scopedStore, question)
	s.cfg.Logger.Debug("retrieved initial relevant docs", "count", len(relevantDocs))

	sparseQuery, err := sparse.GenerateSparseVector(ctx, question)
	var retriever schema.Retriever
	if err != nil {
		s.cfg.Logger.Warn("failed to generate sparse query", "error", err)
		retriever = &hybridRetriever{
			store:     scopedStore,
			archDocs:  relevantDocs,
			baseLimit: similarityLimit,
		}
	} else {
		retriever = &hybridRetriever{
			store:     scopedStore,
			archDocs:  relevantDocs,
			sparse:    sparseQuery,
			baseLimit: similarityLimit,
		}
	}

	if s.cfg.ValidatorLLM != nil {
		return s.answerWithValidation(ctx, retriever, question, history)
	}

	return s.answerWithoutValidation(ctx, retriever, question, history)
}

func (s *QAService) retrieveRelevantDocs(ctx context.Context, store storage.ScopedVectorStore, question string) []schema.Document {
	// Stage 1: Always retrieve architecture summaries (existing logic)
	docs := s.retrieveArchSummaries(ctx, store, question)

	// Stage 2: If keywords are present, retrieve definition chunks
	// These explain how things are defined and structured, crucial for system-internal questions
	keywords := []string{"symbol", "scan", "indexing", "definition"}
	hasKeyword := false
	lowerQuestion := strings.ToLower(question)
	for _, kw := range keywords {
		if strings.Contains(lowerQuestion, kw) {
			hasKeyword = true
			break
		}
	}

	if !hasKeyword {
		return docs
	}

	s.cfg.Logger.Debug("keyword detected, retrieving additional definition chunks")
	defDocs, err := store.SimilaritySearch(ctx, question, archResultLimit,
		vectorstores.WithFilters(map[string]any{"chunk_type": "definition"}))
	if err != nil {
		s.cfg.Logger.Warn("failed to retrieve definition chunks", "error", err)
		return docs
	}

	return deduplicateDocs(append(docs, defDocs...))
}

func (s *QAService) retrieveArchSummaries(ctx context.Context, store storage.ScopedVectorStore, question string) []schema.Document {
	paths := s.extractPaths(question)
	if len(paths) == 0 {
		docs, err := store.SimilaritySearch(ctx, question, archResultLimit,
			vectorstores.WithFilters(map[string]any{"chunk_type": "arch"}))
		if err != nil {
			s.cfg.Logger.Warn("failed to retrieve general arch summaries", "error", err)
			return nil
		}
		return docs
	}

	var allArchDocs []schema.Document
	for _, path := range paths {
		if len(allArchDocs) >= archResultLimit {
			break
		}
		docs, err := store.SimilaritySearch(ctx, path, 1,
			vectorstores.WithFilters(map[string]any{
				"chunk_type": "arch",
				"source":     path,
			}))
		if err != nil {
			s.cfg.Logger.Warn("failed to retrieve arch summary for path", "path", path, "error", err)
			continue
		}
		allArchDocs = append(allArchDocs, docs...)
	}
	return allArchDocs
}

func (s *QAService) extractPaths(question string) []string {
	matches := pathPattern.FindAllStringSubmatch(question, -1)
	seen := make(map[string]bool)
	var paths []string
	for _, match := range matches {
		path := strings.Trim(match[1], ` "'`)
		if !seen[path] {
			seen[path] = true
			paths = append(paths, path)
		}
	}
	return paths
}

// answerWithValidation uses ValidatingRetrievalQA which validates retrieved chunks
// before passing to the generator. History is prepended to the question so the
// model has conversational context even though the chain doesn't natively support it.
func (s *QAService) answerWithValidation(ctx context.Context, retriever schema.Retriever, question string, history []string) (string, error) {
	s.cfg.Logger.Debug("answering with validation")

	questionWithHistory := question
	if len(history) > 0 {
		questionWithHistory = strings.Join(history, "\n") + "\n" + question
	}

	chain, err := chains.NewValidatingRetrievalQA(
		retriever,
		s.cfg.GeneratorLLM,
		chains.WithValidator(s.cfg.ValidatorLLM),
		chains.WithLogger(s.cfg.Logger),
	)
	if err != nil {
		return "", fmt.Errorf("failed to create validating retrieval QA chain: %w", err)
	}

	answer, err := chain.Call(ctx, questionWithHistory)
	if err != nil {
		return "", fmt.Errorf("validating QA chain failed: %w", err)
	}

	s.cfg.Logger.Debug("answer with validation generated", "answer_len", len(answer))
	return answer, nil
}

func (s *QAService) answerWithoutValidation(ctx context.Context, retriever schema.Retriever, question string, history []string) (string, error) {
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
