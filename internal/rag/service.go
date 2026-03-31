package rag

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/sevigo/goframe/contextpacker"
	"github.com/sevigo/goframe/embeddings/sparse"
	sparsecode "github.com/sevigo/goframe/embeddings/sparse/code"
	"github.com/sevigo/goframe/httpclient"
	"github.com/sevigo/goframe/llms"
	"github.com/sevigo/goframe/llms/gemini"
	"github.com/sevigo/goframe/llms/ollama"
	"github.com/sevigo/goframe/parsers"
	"github.com/sevigo/goframe/schema"
	"github.com/sevigo/goframe/textsplitter"
	"github.com/sevigo/goframe/vectorstores"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/core"
	internalgithub "github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/llm"
	"github.com/sevigo/code-warden/internal/rag/contextpkg"
	indexpkg "github.com/sevigo/code-warden/internal/rag/index"
	questionpkg "github.com/sevigo/code-warden/internal/rag/question"
	reviewpkg "github.com/sevigo/code-warden/internal/rag/review"
	"github.com/sevigo/code-warden/internal/storage"
)

// Service is the main RAG pipeline interface for indexing, review, and Q&A.
type Service interface {
	SetupRepoContext(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, repoPath string, progressFn indexpkg.ProgressFunc) error
	UpdateRepoContext(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, repoPath string, filesToProcess, filesToDelete []string, progressFn indexpkg.ProgressFunc) error
	SyncRepoIndex(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, updateResult *core.UpdateResult, progressFn indexpkg.ProgressFunc) error
	GenerateReview(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, event *core.GitHubEvent, diff string, changedFiles []internalgithub.ChangedFile) (*core.StructuredReview, string, error)
	GenerateReReview(ctx context.Context, repo *storage.Repository, event *core.GitHubEvent, originalReview *core.Review, ghClient internalgithub.Client, changedFiles []internalgithub.ChangedFile) (*core.StructuredReview, string, error)
	AnswerQuestion(ctx context.Context, collectionName, embedderModelName, question string, history []string) (string, error)
	ExplainPath(ctx context.Context, collectionName, embedderModelName, path string) (string, error)
	ProcessFile(ctx context.Context, repoPath, file string) []schema.Document
	GenerateComparisonSummaries(ctx context.Context, models []string, repoPath string, relPaths []string) (map[string]map[string]string, error)
	GenerateConsensusReview(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, event *core.GitHubEvent, models []string, diff string, changedFiles []internalgithub.ChangedFile) (*core.StructuredReview, string, error)
	GenerateProjectContext(ctx context.Context, collectionName, embedderModelName string) (string, error)
	GenerateArchSummaries(ctx context.Context, collectionName, embedderModelName, repoPath string, targetPaths []string) error
	GetTextSplitter() textsplitter.TextSplitter
}

// ttlCacheEntry holds a cached value with an expiry timestamp.
type ttlCacheEntry struct {
	value     any
	expiresAt time.Time
}

// ttlCache is a simple bounded cache with TTL-based eviction.
// It is safe for concurrent use.
type ttlCache struct {
	mu      sync.Mutex
	entries map[string]ttlCacheEntry
	ttl     time.Duration
	maxSize int
}

func newTTLCache(ttl time.Duration, maxSize int) *ttlCache {
	return &ttlCache{
		entries: make(map[string]ttlCacheEntry, maxSize),
		ttl:     ttl,
		maxSize: maxSize,
	}
}

func (c *ttlCache) Load(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if time.Now().After(entry.expiresAt) {
		delete(c.entries, key)
		return nil, false
	}
	return entry.value, true
}

func (c *ttlCache) Store(key string, value any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Evict expired entries if at capacity
	if len(c.entries) >= c.maxSize {
		now := time.Now()
		for k, e := range c.entries {
			if now.After(e.expiresAt) {
				delete(c.entries, k)
			}
		}
		// If still at capacity after evicting expired, drop oldest
		if len(c.entries) >= c.maxSize {
			var oldestKey string
			var oldestTime time.Time
			for k, e := range c.entries {
				if oldestKey == "" || e.expiresAt.Before(oldestTime) {
					oldestKey = k
					oldestTime = e.expiresAt
				}
			}
			delete(c.entries, oldestKey)
		}
	}
	c.entries[key] = ttlCacheEntry{value: value, expiresAt: time.Now().Add(c.ttl)}
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
	contextBuilder contextpkg.Builder
	llmGroup       singleflight.Group
	qaService      *questionpkg.QAService
	indexer        *indexpkg.Indexer
	reviewService  *reviewpkg.Service
	logger         *slog.Logger
	llmCache       *ttlCache // modelName -> LLM instance
}

// NewService creates and returns a new RAG [Service].
//
//nolint:funlen // Complex initialization with multiple component configs
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
	// Register code-aware sparse provider for hybrid search.
	// Uses camelCase/snake_case splitting + FNV hashing instead of the BGE text tokenizer,
	// which treats identifiers like processPayment and XMLParser as better search signals.
	sparse.RegisterProvider(sparsecode.NewCodeSparseProvider())

	// Log hybrid search configuration
	if cfg.AI.EnableHybrid {
		logger.Info("Hybrid search enabled", "sparse_vector_name", cfg.AI.SparseVectorName)
	} else {
		logger.Info("Hybrid search disabled, using dense vectors only")
	}

	// Get token budget from config, with fallback.
	tokenBudget := cfg.AI.ContextTokenBudget
	if tokenBudget <= 0 {
		tokenBudget = 16000 // Default for 128K context models
	}

	// Create context packer with configurable token budget.
	contextPacker, err := newContextPacker(gen, tokenBudget, logger)
	if err != nil {
		return nil, err
	}

	qaCfg := questionpkg.Config{
		VectorStore:  vs,
		GeneratorLLM: gen,
		PromptMgr:    promptMgr,
		Logger:       logger,
		ContextFormat: func(docs []schema.Document) string {
			if len(docs) == 0 {
				return ""
			}
			res, err := contextPacker.Pack(context.Background(), docs)
			if err != nil {
				logger.Warn("Failed to pack context, returning empty", "error", err)
				return ""
			}
			return res.Content
		},
	}

	indexerCfg := indexpkg.Config{
		Store:          dbStore,
		VectorStore:    vs,
		ParserRegistry: pr,
		Splitter:       splitter,
		Logger:         logger,
		EmbedderModel:  cfg.AI.EmbedderModel,
		LLM:            gen,
		PromptMgr:      promptMgr,
	}

	r := &ragService{
		cfg:            cfg,
		promptMgr:      promptMgr,
		vectorStore:    vs,
		store:          dbStore,
		generatorLLM:   gen,
		reranker:       reranker,
		parserRegistry: pr,
		splitter:       splitter,
		llmGroup:       singleflight.Group{},
		logger:         logger,
		qaService:      questionpkg.NewService(qaCfg),
		indexer:        indexpkg.New(indexerCfg),
		llmCache:       newTTLCache(1*time.Hour, 20),
	}

	contextCfg := contextpkg.Config{
		AIConfig:       cfg.AI,
		VectorStore:    vs,
		PromptMgr:      promptMgr,
		ParserRegistry: pr,
		GeneratorLLM:   gen,
		GetLLM:         r.getOrCreateLLM,
		Reranker:       reranker,
		ContextPacker:  contextPacker,
		HyDECache:      newTTLCache(30*time.Minute, 500),
		Logger:         logger.With("component", "context_builder"),
	}
	r.contextBuilder = contextpkg.NewBuilder(contextCfg)

	reviewCfg := reviewpkg.Config{
		VectorStore:            vs,
		PromptMgr:              promptMgr,
		GeneratorLLM:           gen,
		GetLLM:                 r.getOrCreateLLM,
		Logger:                 logger,
		ConsensusTimeout:       cfg.AI.ConsensusTimeout,
		ConsensusQuorum:        cfg.AI.ConsensusQuorum,
		BuildContext:           r.contextBuilder.BuildRelevantContext,
		BuildContextWithImpact: r.contextBuilder.BuildRelevantContextWithImpact,
		EmbedderModel:          cfg.AI.EmbedderModel,
	}

	// Wire Phase 2 investigator when a fast model is configured.
	if cfg.AI.FastModel != "" {
		investigator := reviewpkg.NewInvestigator(
			vs,
			promptMgr,
			cfg.AI.EmbedderModel,
			cfg.AI.FastModel,
			r.getOrCreateLLM,
			logger.With("component", "investigator"),
		)
		reviewCfg.Investigate = investigator.Investigate
	}

	r.reviewService = reviewpkg.NewService(reviewCfg)

	return r, nil
}

func newContextPacker(gen llms.Model, tokenBudget int, logger *slog.Logger) (*contextpacker.Packer, error) {
	tokenizer := llm.AsTokenizer(gen)
	cp, err := contextpacker.New(tokenizer, tokenBudget,
		contextpacker.WithTemplate(contextpacker.CompactTemplate),
		contextpacker.WithLogger(logger),
	)
	if err == nil {
		return cp, nil
	}

	logger.Warn("failed to create context packer with model tokenizer, using estimation fallback", "error", err)
	cp, err = contextpacker.New(llm.NewEstimatingTokenizer(), tokenBudget,
		contextpacker.WithTemplate(contextpacker.CompactTemplate),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize context packer: %w", err)
	}
	return cp, nil
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

	// Check cache first
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

			clientCfg := httpclient.NewConfig(
				httpclient.WithResponseHeaderTimeout(headerTimeout),
			)
			clientCfg.Timeout = 0 // Disable absolute client timeout, rely on Context and ResponseHeaderTimeout

			newLLM, err = ollama.New(
				ollama.WithServerURL(r.cfg.AI.OllamaHost),
				ollama.WithAPIKey(r.cfg.AI.OllamaAPIKey),
				ollama.WithModel(modelName),
				ollama.WithHTTPClient(httpclient.NewClient(clientCfg)),
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

// AnswerQuestion retrieves relevant documents and generates an answer via LLM.
func (r *ragService) AnswerQuestion(ctx context.Context, collectionName, embedderModelName, question string, history []string) (string, error) {
	// Dynamically fetch the validator LLM if configured
	var validatorLLM llms.Model
	var err error
	if r.cfg.AI.FastModel != "" {
		validatorLLM, err = r.getOrCreateLLM(ctx, r.cfg.AI.FastModel)
		if err != nil {
			r.logger.Warn("failed to create validator LLM for QA, falling back to basic QA", "error", err)
			validatorLLM = nil
		}
	}

	qaCfg := questionpkg.Config{
		VectorStore:   r.vectorStore,
		GeneratorLLM:  r.generatorLLM,
		ValidatorLLM:  validatorLLM,
		PromptMgr:     r.promptMgr,
		Logger:        r.logger,
		ContextFormat: r.contextBuilder.BuildContextForPrompt,
	}

	svc := questionpkg.NewService(qaCfg)
	return svc.AnswerQuestion(ctx, collectionName, embedderModelName, question, history)
}

func (r *ragService) ExplainPath(ctx context.Context, collectionName, embedderModelName, path string) (string, error) {
	r.logger.Info("explaining path", "collection", collectionName, "path", path)
	scopedStore := r.vectorStore.ForRepo(collectionName, embedderModelName)

	docs, err := scopedStore.SimilaritySearch(ctx, path, 1,
		vectorstores.WithFilters(map[string]any{
			"chunk_type": "arch",
			"source":     path,
		}))
	if err != nil {
		return "", fmt.Errorf("failed to retrieve arch context: %w", err)
	}

	if len(docs) == 0 {
		return fmt.Sprintf("No architectural context found for path: %s\n\nTry a broader path or type your question directly.", path), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Architecture: %s\n\n", path)
	for _, doc := range docs {
		fmt.Fprintf(&b, "%s\n\n", doc.PageContent)
		if source, ok := doc.Metadata["source"].(string); ok {
			fmt.Fprintf(&b, "_Source: %s_\n\n", source)
		}
	}
	return b.String(), nil
}

func (r *ragService) SetupRepoContext(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, repoPath string, progressFn indexpkg.ProgressFunc) error {
	err := r.indexer.SetupRepoContext(ctx, repoConfig, repo, repoPath, progressFn)
	if err != nil {
		return err
	}
	if err := r.GenerateArchSummaries(ctx, repo.QdrantCollectionName, r.cfg.AI.EmbedderModel, repoPath, nil); err != nil {
		r.logger.Warn("failed to generate architectural summaries, continuing without them", "error", err)
	}

	if err := r.contextBuilder.GeneratePackageSummaries(ctx, repo.QdrantCollectionName, r.cfg.AI.EmbedderModel); err != nil {
		r.logger.Warn("failed to generate package summaries, continuing without them", "error", err)
	}

	r.logger.Info("📉 Synthesizing global Project Context document", "repo", repo.FullName)
	projectContext, err := r.GenerateProjectContext(ctx, repo.QdrantCollectionName, r.cfg.AI.EmbedderModel)
	if err != nil {
		r.logger.Warn("failed to synthesize project context, continuing without it", "error", err)
	} else if projectContext != "" {
		repo.GeneratedContext = projectContext
		repo.ContextUpdatedAt = sql.NullTime{Time: time.Now(), Valid: true}
		if err := r.store.UpdateRepository(ctx, repo); err != nil {
			r.logger.Error("failed to save generated context to database", "error", err)
		} else {
			r.logger.Info("✅ Global Project Context document saved to database", "length", len(projectContext))
		}
	}
	return nil
}

func (r *ragService) UpdateRepoContext(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, repoPath string, filesToProcess, filesToDelete []string, progressFn indexpkg.ProgressFunc) error {
	err := r.indexer.UpdateRepoContext(ctx, repoConfig, repo, repoPath, filesToProcess, filesToDelete, progressFn)
	if err != nil {
		return err
	}
	// Trigger targeted arch summary re-generation
	if err := r.GenerateArchSummaries(ctx, repo.QdrantCollectionName, r.cfg.AI.EmbedderModel, repoPath, append(filesToProcess, filesToDelete...)); err != nil {
		r.logger.Warn("failed to update architectural summaries after sync", "error", err)
	}

	// Regenerate package summaries after incremental update
	// This fetches all TOC/definition chunks and rebuilds package-level summaries
	if err := r.contextBuilder.GeneratePackageSummaries(ctx, repo.QdrantCollectionName, r.cfg.AI.EmbedderModel); err != nil {
		r.logger.Warn("failed to regenerate package summaries after sync", "error", err)
	}

	return nil
}

// SyncRepoIndex handles the common pattern of syncing repository index based on update result.
// It chooses between initial full indexing and incremental update based on the update result.
func (r *ragService) SyncRepoIndex(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, updateResult *core.UpdateResult, progressFn indexpkg.ProgressFunc) error {
	switch {
	case updateResult.IsInitialClone:
		r.logger.Info("performing initial full indexing", "repo", repo.FullName)
		return r.SetupRepoContext(ctx, repoConfig, repo, updateResult.RepoPath, progressFn)
	case len(updateResult.FilesToAddOrUpdate) > 0 || len(updateResult.FilesToDelete) > 0:
		r.logger.Info("performing incremental indexing",
			"repo", repo.FullName,
			"added_or_updated", len(updateResult.FilesToAddOrUpdate),
			"deleted", len(updateResult.FilesToDelete),
		)
		return r.UpdateRepoContext(ctx, repoConfig, repo, updateResult.RepoPath, updateResult.FilesToAddOrUpdate, updateResult.FilesToDelete, progressFn)
	default:
		r.logger.Info("no changes detected, skipping indexing", "repo", repo.FullName)
		return nil
	}
}

func (r *ragService) ProcessFile(ctx context.Context, repoPath, file string) []schema.Document {
	return r.indexer.ProcessFile(ctx, repoPath, file)
}

func (r *ragService) GenerateReview(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, event *core.GitHubEvent, diff string, changedFiles []internalgithub.ChangedFile) (*core.StructuredReview, string, error) {
	return r.reviewService.GenerateReview(ctx, repoConfig, repo, event, diff, changedFiles)
}

func (r *ragService) GenerateReReview(ctx context.Context, repo *storage.Repository, event *core.GitHubEvent, originalReview *core.Review, ghClient internalgithub.Client, changedFiles []internalgithub.ChangedFile) (*core.StructuredReview, string, error) {
	return r.reviewService.GenerateReReview(ctx, repo, event, originalReview, ghClient, changedFiles)
}

func (r *ragService) GenerateConsensusReview(ctx context.Context, repoConfig *core.RepoConfig, repo *storage.Repository, event *core.GitHubEvent, models []string, diff string, changedFiles []internalgithub.ChangedFile) (*core.StructuredReview, string, error) {
	return r.reviewService.GenerateConsensusReview(ctx, repoConfig, repo, event, models, diff, changedFiles)
}

// GenerateComparisonSummaries generates architectural summaries for multiple directories.
func (r *ragService) GenerateComparisonSummaries(ctx context.Context, models []string, repoPath string, relPaths []string) (map[string]map[string]string, error) {
	return r.contextBuilder.GenerateComparisonSummaries(ctx, models, repoPath, relPaths)
}

// GenerateArchSummaries generates architectural summaries for the repository.
func (r *ragService) GenerateArchSummaries(ctx context.Context, collectionName, embedderModelName, repoPath string, targetPaths []string) error {
	return r.contextBuilder.GenerateArchSummaries(ctx, collectionName, embedderModelName, repoPath, targetPaths)
}

// GenerateProjectContext synthesizes all architectural summaries into a global project context.
func (r *ragService) GenerateProjectContext(ctx context.Context, collectionName, embedderModelName string) (string, error) {
	return r.contextBuilder.GenerateProjectContext(ctx, collectionName, embedderModelName)
}
