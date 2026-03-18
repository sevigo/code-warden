package contextpkg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"

	"github.com/sevigo/goframe/schema"
	"github.com/sevigo/goframe/vectorstores"
	"golang.org/x/sync/errgroup"

	internalgithub "github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/rag/detect"
	"github.com/sevigo/code-warden/internal/storage"
)

// Builder defines the interface for building context.
type Builder interface {
	BuildRelevantContext(ctx context.Context, collectionName, embedderModelName, repoPath string, changedFiles []internalgithub.ChangedFile, prDescription string) (string, string)
	BuildContextForPrompt(docs []schema.Document) string
	GenerateArchSummaries(ctx context.Context, collectionName, embedderModelName, repoPath string, targetPaths []string) error
	GenerateComparisonSummaries(ctx context.Context, models []string, repoPath string, relPaths []string) (map[string]map[string]string, error)
	GenerateProjectContext(ctx context.Context, collectionName, embedderModelName string) (string, error)
}

// builderImpl implements context building logic.
type builderImpl struct {
	cfg Config
}

// NewBuilder creates a new [Builder] instance.
func NewBuilder(cfg Config) Builder {
	return &builderImpl{cfg: cfg}
}

// BuildRelevantContext performs similarity searches to gather context for a review.
func (b *builderImpl) BuildRelevantContext(ctx context.Context, collectionName, embedderModelName, repoPath string, changedFiles []internalgithub.ChangedFile, prDescription string) (string, string) {
	if len(changedFiles) == 0 {
		return "", ""
	}

	const defaultMaxContextFiles = 20
	if len(changedFiles) > defaultMaxContextFiles {
		b.cfg.Logger.Warn("truncating context files", "total", len(changedFiles), "limit", defaultMaxContextFiles)
		changedFiles = changedFiles[:defaultMaxContextFiles]
	}

	scopedStore := b.cfg.VectorStore.ForRepo(collectionName, embedderModelName)
	results := b.buildContextConcurrently(ctx, collectionName, embedderModelName, repoPath, prDescription, changedFiles, scopedStore)

	b.cfg.Logger.Debug("raw context gathered",
		"arch_found", results.archContext != "",
		"definitions_found", results.definitionsContext != "",
		"impact_docs_count", len(results.impactDocs),
		"description_docs_count", len(results.descriptionDocs),
		"hyde_results_count", len(results.hydeResults),
	)

	allDocs := mergeAndDedup(append(results.impactDocs, results.descriptionDocs...), b.getDocKey)

	var impactContext, descriptionContext string
	if len(allDocs) > 0 {
		var seenDocs sync.Map
		impactContext, descriptionContext = b.splitAndFormatDocs(ctx, allDocs, results.descriptionDocs, prDescription, &seenDocs)
	}

	fullContext := b.assembleContext(ctx, results.archContext, impactContext, descriptionContext, results.definitionsContext, results.hydeResults, results.hydeIndices, changedFiles)
	return fullContext, results.definitionsContext
}

type contextResults struct {
	archContext        string
	definitionsContext string
	impactDocs         []schema.Document
	descriptionDocs    []schema.Document
	hydeResults        [][]schema.Document
	hydeIndices        []int
}

func (b *builderImpl) buildContextConcurrently(
	ctx context.Context, collectionName, embedderModelName, repoPath, prDescription string,
	changedFiles []internalgithub.ChangedFile, scopedStore storage.ScopedVectorStore,
) *contextResults {
	results := &contextResults{}
	g, ctx := errgroup.WithContext(ctx)

	g.SetLimit(3)

	g.Go(func() error {
		arch, err := b.gatherArchContextSafe(ctx, scopedStore, changedFiles)
		results.archContext = arch
		return err
	})

	if b.cfg.AIConfig.EnableHyDE {
		g.Go(func() error {
			res, indices, err := b.gatherHyDEContext(ctx, collectionName, embedderModelName, changedFiles)
			results.hydeResults = res
			results.hydeIndices = indices
			return err
		})
	}

	g.Go(func() error {
		docs, err := b.gatherImpactDocs(ctx, scopedStore, repoPath, changedFiles)
		results.impactDocs = filterTestDocs(docs)
		return err
	})

	if prDescription != "" {
		g.Go(func() error {
			docs, err := b.gatherDescriptionDocs(ctx, collectionName, embedderModelName, prDescription)
			results.descriptionDocs = filterTestDocs(docs)
			return err
		})
	}

	g.Go(func() error {
		defs, err := b.gatherDefinitionsContext(ctx, scopedStore, changedFiles)
		results.definitionsContext = defs
		return err
	})

	if err := g.Wait(); err != nil {
		b.cfg.Logger.Error("buildContextConcurrently: one or more tasks failed", "error", err)
	}

	return results
}

func (b *builderImpl) filterValidDescriptionDocs(ctx context.Context, descKeys map[string]schema.Document, seen *sync.Map, prDescription string) map[string]bool {
	var toValidate []schema.Document
	var snippets []string
	for source, d := range descKeys {
		if _, loaded := seen.Load(source); !loaded {
			toValidate = append(toValidate, d)
			snippets = append(snippets, b.getDocContent(d))
		}
	}

	relevanceMap := make(map[int]bool, len(toValidate))
	if len(snippets) > 0 && prDescription != "" {
		validatorLLM, err := b.cfg.GetLLM(ctx, b.cfg.AIConfig.FastModel)
		if err == nil {
			v := detect.NewSnippetValidator(validatorLLM, b.cfg.PromptMgr)
			relevanceMap = v.ValidateBatch(ctx, snippets, prDescription)
		} else {
			for i := range snippets {
				relevanceMap[i] = true
			}
		}
	}

	validSources := make(map[string]bool, len(toValidate))
	for i, doc := range toValidate {
		if relevanceMap[i] {
			source, _ := doc.Metadata["source"].(string)
			validSources[source] = true
		}
	}

	b.cfg.Logger.Debug("description snippets validated", "total_candidates", len(toValidate), "valid_count", len(validSources))
	return validSources
}

func (b *builderImpl) gatherDescriptionDocs(ctx context.Context, collection, embedder, description string) ([]schema.Document, error) {
	b.cfg.Logger.Info("stage started", "name", "DescriptionContext")
	scopedStore := b.cfg.VectorStore.ForRepo(collection, embedder)

	queryLLM, err := b.cfg.GetLLM(ctx, b.cfg.AIConfig.FastModel)
	if err != nil {
		queryLLM = b.cfg.GeneratorLLM
	}

	retriever := vectorstores.MultiQueryRetriever{
		Store:         scopedStore,
		LLM:           queryLLM,
		NumDocuments:  10,
		Count:         3,
		SparseGenFunc: b.generateSparseVectorFunc("DescriptionContext"),
	}

	allDocs, err := retriever.GetRelevantDocuments(ctx, description)
	if err != nil {
		b.cfg.Logger.Warn("multi-query retrieval failed", "error", err)
		return nil, err
	}

	b.cfg.Logger.Info("stage completed", "name", "DescriptionContext", "retrieved", len(allDocs))
	return allDocs, nil
}

func (b *builderImpl) assembleContext(ctx context.Context, arch, impact, description, definitions string, hyde [][]schema.Document, indices []int, files []internalgithub.ChangedFile) string {
	docs := b.buildContextDocuments(arch, impact, description, definitions, hyde, indices, files)

	if b.cfg.ContextPacker == nil {
		b.cfg.Logger.Error("context packer not initialized, using limited fallback")
		return b.fallbackConcat(docs)
	}

	result, err := b.cfg.ContextPacker.Pack(ctx, docs)
	if err != nil {
		b.cfg.Logger.Error("context packer failed, using limited fallback - token budget may not be enforced", "error", err)
		return b.fallbackConcat(docs)
	}

	b.cfg.Logger.Info("relevant context built",
		"changed_files", len(files),
		"arch_len", len(arch),
		"impact_len", len(impact),
		"definitions_len", len(definitions),
		"hyde_results_count", len(hyde),
		"total_tokens", result.TokenStats.TotalTokens,
		"documents_packed", result.TokenStats.DocumentsPacked,
		"documents_considered", result.TokenStats.DocumentsConsidered,
		"truncated", result.Truncated,
	)

	return result.Content
}

// hashPatch returns a 128-bit hex hash of the patch content for cache keying.
func (b *builderImpl) hashPatch(patch string) string {
	hash := sha256.Sum256([]byte(patch))
	return hex.EncodeToString(hash[:16])
}
