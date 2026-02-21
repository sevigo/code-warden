package rag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"regexp"
	"sync"

	"github.com/sevigo/goframe/llms"
	"github.com/sevigo/goframe/llms/gemini"
	"github.com/sevigo/goframe/llms/ollama"
	"github.com/sevigo/goframe/parsers"
	"github.com/sevigo/goframe/schema"
	"github.com/sevigo/goframe/textsplitter"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/core"
	internalgithub "github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/llm"
	"github.com/sevigo/code-warden/internal/storage"
)

// Pre-compiled regexes for comment cleaning and symbol extraction to avoid recompilation on each call
var (
	statusRegex     = regexp.MustCompile(`(?i)\*\*status:\*\*\s*(unresolved|partial|fixed|new critical bug)\s*`)
	obsRegex        = regexp.MustCompile(`(?i)\*\*observation:\*\*`)
	rootCauseRegex  = regexp.MustCompile(`(?i)\*\*root cause:\*\*`)
	fixRegex        = regexp.MustCompile(`(?i)\*\*fix:\*\*`)
	whitespaceRegex = regexp.MustCompile(`\s+`)

	// Symbol extraction patterns for Go code
	symbolTypeDefRegex      = regexp.MustCompile(`(?m)^\+?\s*type\s+(\w+)\s+(?:struct|interface)`)
	symbolFuncDefRegex      = regexp.MustCompile(`(?m)^\+?\s*func\s+(?:\([^)]+\))?\s*(\w+)`)
	symbolVarDeclRegex      = regexp.MustCompile(`(?m)\bvar\s+\w+\s+(\w+)`)
	symbolTypeAssertRegex   = regexp.MustCompile(`(?m)\b([A-Z]\w*)\{`)
	symbolExportedTypeRegex = regexp.MustCompile(`\b([A-Z]\w+)(?:\.|\{)`)
)

// Service defines the core operations for our Retrieval-Augmented Generation (RAG) pipeline.
type Service interface {
	SetupRepoContext(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, repoPath string) error
	UpdateRepoContext(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, repoPath string, filesToProcess, filesToDelete []string) error
	GenerateReview(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, event *core.GitHubEvent, diff string, changedFiles []internalgithub.ChangedFile) (*core.StructuredReview, string, error)
	GenerateReReview(ctx context.Context, repo *storage.Repository, event *core.GitHubEvent, originalReview *core.Review, ghClient internalgithub.Client, changedFiles []internalgithub.ChangedFile) (*core.StructuredReview, string, error)
	AnswerQuestion(ctx context.Context, collectionName, embedderModelName, question string, history []string) (string, error)
	ProcessFile(repoPath, file string) []schema.Document
	GenerateComparisonSummaries(ctx context.Context, models []string, repoPath string, relPaths []string) (map[string]map[string]string, error)
	GenerateConsensusReview(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, event *core.GitHubEvent, models []string, diff string, changedFiles []internalgithub.ChangedFile) (*core.StructuredReview, string, error)
	GetTextSplitter() textsplitter.TextSplitter
}

type ragService struct {
	cfg            *config.Config
	promptMgr      *llm.PromptManager
	vectorStore    storage.VectorStore
	store          storage.Store
	generatorLLM   llms.Model
	reranker       schema.Reranker
	parserRegistry parsers.ParserRegistry
	splitter       textsplitter.TextSplitter
	logger         *slog.Logger
	hydeCache      sync.Map // map[string]string: patchHash -> hydeSnippet
}

// NewService creates a new Service instance with a vector store, LLM model,
// parser registry, and logger. This service powers the indexing and code review flow.
func NewService(
	cfg *config.Config,
	promptMgr *llm.PromptManager,
	vs storage.VectorStore,
	dbStore storage.Store,
	gen llms.Model,
	reranker schema.Reranker,
	pr parsers.ParserRegistry,
	splitter textsplitter.TextSplitter,
	logger *slog.Logger,
) Service {
	return &ragService{
		cfg:            cfg,
		promptMgr:      promptMgr,
		vectorStore:    vs,
		store:          dbStore,
		generatorLLM:   gen,
		reranker:       reranker,
		parserRegistry: pr,
		splitter:       splitter,
		logger:         logger,
	}
}

func (r *ragService) GetTextSplitter() textsplitter.TextSplitter {
	return r.splitter
}

func (r *ragService) getOrCreateLLM(modelName string) (llms.Model, error) {
	// For now, just return the initialized generator if model matches or if we don't support dynamic switching yet.
	// This is a simplification to fix the build.
	if modelName == r.cfg.AI.GeneratorModel {
		return r.generatorLLM, nil
	}

	// Create new instance if needed (simplified fallback)
	r.logger.Info("creating new LLM instance on the fly", "model", modelName)
	if r.cfg.AI.LLMProvider == "gemini" {
		return gemini.New(context.Background(), gemini.WithModel(modelName), gemini.WithAPIKey(r.cfg.AI.GeminiAPIKey))
	}
	// Fallback/Default to Ollama
	return ollama.New(
		ollama.WithServerURL(r.cfg.AI.OllamaHost),
		ollama.WithModel(modelName),
	)
}

func (r *ragService) generateResponseWithPrompt(ctx context.Context, event *core.GitHubEvent, promptKey llm.PromptKey, promptData any) (string, error) {
	// Try using the main generator first
	llmModel, err := r.getOrCreateLLM(r.cfg.AI.GeneratorModel)
	if err != nil {
		r.logger.Error("failed to get generator LLM", "error", err)
		return "", fmt.Errorf("failed to get LLM model: %w", err)
	}

	prompt, err := r.promptMgr.Render(promptKey, promptData)
	if err != nil {
		return "", fmt.Errorf("could not render prompt '%s': %w", promptKey, err)
	}

	r.logger.Info("calling LLM for response generation",
		"repo", event.RepoFullName,
		"pr", event.PRNumber,
		"prompt_key", promptKey,
	)

	response, err := llmModel.Call(ctx, prompt)
	if err != nil {
		return "", fmt.Errorf("LLM generation failed for prompt '%s': %w", promptKey, err)
	}

	r.logger.Info("LLM response generated successfully", "chars", len(response))
	return response, nil
}

// hashPatch computes a short hash of the patch content for caching.
func (r *ragService) hashPatch(patch string) string {
	hash := sha256.Sum256([]byte(patch))
	return hex.EncodeToString(hash[:8])
}
