package rag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"regexp"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/sevigo/goframe/contextpacker"
	"github.com/sevigo/goframe/embeddings/sparse"
	"github.com/sevigo/goframe/httpclient"
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

// Pre-compiled regexes for review comment cleaning.
var (
	statusRegex     = regexp.MustCompile(`(?i)\*\*status:\*\*\s*(unresolved|partial|fixed|new critical bug)\s*`)
	obsRegex        = regexp.MustCompile(`(?i)\*\*observation:\*\*`)
	rootCauseRegex  = regexp.MustCompile(`(?i)\*\*root cause:\*\*`)
	fixRegex        = regexp.MustCompile(`(?i)\*\*fix:\*\*`)
	whitespaceRegex = regexp.MustCompile(`\s+`)
)

// Pre-compiled regexes for Go symbol extraction from diffs.
var (
	symbolTypeDefRegex      = regexp.MustCompile(`(?m)^\+?\s*type\s+(\w+)\s+(?:struct|interface)`)
	symbolFuncDefRegex      = regexp.MustCompile(`(?m)^\+?\s*func\s+(?:\([^)]+\))?\s*(\w+)`)
	symbolVarDeclRegex      = regexp.MustCompile(`(?m)\bvar\s+\w+\s+(\w+)`)
	symbolTypeAssertRegex   = regexp.MustCompile(`(?m)\b([A-Z]\w*)\{`)
	symbolExportedTypeRegex = regexp.MustCompile(`\b([A-Z]\w+)(?:\.|\{)`)
)

// Service is the main RAG pipeline interface for indexing, review, and Q&A.
type Service interface {
	SetupRepoContext(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, repoPath string) error
	UpdateRepoContext(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, repoPath string, filesToProcess, filesToDelete []string) error
	GenerateReview(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, event *core.GitHubEvent, diff string, changedFiles []internalgithub.ChangedFile) (*core.StructuredReview, string, error)
	GenerateReReview(ctx context.Context, repo *storage.Repository, event *core.GitHubEvent, originalReview *core.Review, ghClient internalgithub.Client, changedFiles []internalgithub.ChangedFile) (*core.StructuredReview, string, error)
	AnswerQuestion(ctx context.Context, collectionName, embedderModelName, question string, history []string) (string, error)
	ProcessFile(ctx context.Context, repoPath, file string) []schema.Document
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
	contextPacker  *contextpacker.Packer
	llmGroup       singleflight.Group
	logger         *slog.Logger
	hydeCache      sync.Map // map[string]string: patchHash -> hydeSnippet
	llmCache       sync.Map // map[string]llms.Model: modelName -> LLM instance
}

// NewService creates and returns a new RAG [Service].
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
) (Service, error) {
	// Register sparse provider for hybrid search.
	sparse.RegisterProvider(sparse.NewBoWProvider())

	// Get token budget from config, with fallback.
	tokenBudget := cfg.AI.ContextTokenBudget
	if tokenBudget <= 0 {
		tokenBudget = 16000 // Default for 128K context models
	}

	// Create context packer with configurable token budget.
	tokenizer := llm.AsTokenizer(gen)
	contextPacker, err := contextpacker.New(tokenizer, tokenBudget,
		contextpacker.WithTemplate(contextpacker.CompactTemplate),
		contextpacker.WithLogger(logger),
	)
	if err != nil {
		logger.Warn("failed to create context packer with model tokenizer, using estimation fallback", "error", err)
		// Fallback to estimation-based packer
		contextPacker, err = contextpacker.New(llm.NewEstimatingTokenizer(), tokenBudget,
			contextpacker.WithTemplate(contextpacker.CompactTemplate),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize context packer: %w", err)
		}
	}

	return &ragService{
		cfg:            cfg,
		promptMgr:      promptMgr,
		vectorStore:    vs,
		store:          dbStore,
		generatorLLM:   gen,
		reranker:       reranker,
		parserRegistry: pr,
		splitter:       splitter,
		contextPacker:  contextPacker,
		llmGroup:       singleflight.Group{},
		logger:         logger,
	}, nil
}

// GetTextSplitter returns the configured text splitter.
func (r *ragService) GetTextSplitter() textsplitter.TextSplitter {
	return r.splitter
}

// getOrCreateLLM returns an LLM instance for the given model name.
// It uses singleflight to prevent duplicate concurrent creation of the same model.
func (r *ragService) getOrCreateLLM(ctx context.Context, modelName string) (llms.Model, error) {
	// Return the initialized generator if model matches
	if modelName == r.cfg.AI.GeneratorModel {
		return r.generatorLLM, nil
	}

	// Check cache first (read-only lock-free lookup)
	if cached, ok := r.llmCache.Load(modelName); ok {
		if llmModel, valid := cached.(llms.Model); valid {
			return llmModel, nil
		}
	}

	// Dedup concurrent creation for the same model.
	result, err, _ := r.llmGroup.Do(modelName, func() (any, error) {
		// Double-check cache after acquiring the flight.
		if cached, ok := r.llmCache.Load(modelName); ok {
			if llmModel, valid := cached.(llms.Model); valid {
				return llmModel, nil
			}
		}

		r.logger.Info("creating LLM instance", "model", modelName)

		var newLLM llms.Model
		var err error

		if r.cfg.AI.LLMProvider == "gemini" {
			newLLM, err = gemini.New(ctx, gemini.WithModel(modelName), gemini.WithAPIKey(r.cfg.AI.GeminiAPIKey))
		} else {
			// Fallback/Default to Ollama
			headerTimeout, pErr := time.ParseDuration(r.cfg.AI.HTTPResponseHeaderTimeout)
			if pErr != nil {
				r.logger.Warn("invalid http_response_header_timeout, using default",
					"configured", r.cfg.AI.HTTPResponseHeaderTimeout,
					"error", pErr,
				)
				headerTimeout = 120 * time.Second // use default
			}

			newLLM, err = ollama.New(
				ollama.WithServerURL(r.cfg.AI.OllamaHost),
				ollama.WithAPIKey(r.cfg.AI.OllamaAPIKey),
				ollama.WithModel(modelName),
				ollama.WithHTTPClient(httpclient.NewClient(httpclient.NewConfig(
					httpclient.WithResponseHeaderTimeout(headerTimeout),
				))),
				ollama.WithRetryAttempts(3),
				ollama.WithRetryDelay(2*time.Second),
			)
		}

		if err != nil {
			return nil, fmt.Errorf("failed to create LLM for model %s: %w", modelName, err)
		}

		// Store in cache for future use
		r.llmCache.Store(modelName, newLLM)
		return newLLM, nil
	})
	if err != nil {
		return nil, err
	}
	llmModel, ok := result.(llms.Model)
	if !ok {
		return nil, fmt.Errorf("unexpected type from singleflight for model %s", modelName)
	}
	return llmModel, nil
}

// generateResponseWithPrompt renders a prompt template and calls the generator LLM.
func (r *ragService) generateResponseWithPrompt(ctx context.Context, event *core.GitHubEvent, promptKey llm.PromptKey, promptData any) (string, error) {
	llmModel, err := r.getOrCreateLLM(ctx, r.cfg.AI.GeneratorModel)
	if err != nil {
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

// hashPatch returns a 128-bit hex hash of the patch content for cache keying.
func (r *ragService) hashPatch(patch string) string {
	hash := sha256.Sum256([]byte(patch))
	return hex.EncodeToString(hash[:16])
}
