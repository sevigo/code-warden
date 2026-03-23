package contextpkg

import (
	"context"
	"strings"
	"sync"

	"github.com/sevigo/goframe/schema"
	"github.com/sevigo/goframe/vectorstores"

	"github.com/sevigo/code-warden/internal/cryptoutil"
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
	GeneratePackageSummaries(ctx context.Context, collectionName, embedderModelName string) error
}

// builderImpl implements context building logic.
type builderImpl struct {
	cfg            Config
	fileKeywords   []string
	fileKeywordsMu sync.RWMutex
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

	const defaultMaxContextFiles = 50
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

	testCoverageContext := b.formatTestCoverageContext(results.testCoverageDocs)
	fullContext := b.assembleContext(ctx, results.archContext, results.tocContext, results.fileSummaryContext, impactContext, descriptionContext, results.definitionsContext, testCoverageContext, results.packageContext, results.relationContext, results.hydeResults, results.hydeIndices, changedFiles)
	return fullContext, results.definitionsContext
}

type contextResults struct {
	archContext        string
	tocContext         string
	fileSummaryContext string
	definitionsContext string
	impactDocs         []schema.Document
	descriptionDocs    []schema.Document
	hydeResults        [][]schema.Document
	hydeIndices        []int
	testCoverageDocs   []schema.Document
	packageContext     string
	relationContext    string
}

//nolint:gocognit // concurrent context building requires multiple goroutines with error handling
func (b *builderImpl) buildContextConcurrently(
	ctx context.Context, collectionName, embedderModelName, repoPath, prDescription string,
	changedFiles []internalgithub.ChangedFile, scopedStore storage.ScopedVectorStore,
) *contextResults {
	results := &contextResults{}

	// Run FileSummaryContext first to collect keywords for HyDE boosting.
	// This stage is fast (exact filter queries) and must complete before HyDE
	// to ensure keywords are available.
	results.fileSummaryContext = b.gatherFileSummaryContext(ctx, scopedStore, changedFiles)

	// Each stage runs independently. A failure in one stage must not cancel the
	// others — losing arch context because HyDE hit a transient Qdrant error, or
	// vice versa, degrades review quality silently. We log each failure and let
	// remaining stages complete with whatever context they can assemble.
	var wg sync.WaitGroup

	wg.Go(func() {
		arch, err := b.gatherArchContextSafe(ctx, scopedStore, changedFiles)
		if err != nil {
			b.cfg.Logger.Warn("arch context stage failed", "error", err)
		}
		results.archContext = arch
	})

	wg.Go(func() {
		toc, err := b.gatherTOCContext(ctx, scopedStore, changedFiles)
		if err != nil {
			b.cfg.Logger.Warn("TOC context stage failed", "error", err)
		}
		results.tocContext = toc
	})

	if b.cfg.AIConfig.EnableHyDE {
		wg.Go(func() {
			res, indices, err := b.gatherHyDEContext(ctx, collectionName, embedderModelName, changedFiles)
			if err != nil {
				b.cfg.Logger.Warn("HyDE context stage failed", "error", err)
			}
			results.hydeResults = res
			results.hydeIndices = indices
		})
	}

	wg.Go(func() {
		docs, err := b.gatherImpactDocs(ctx, scopedStore, repoPath, changedFiles)
		if err != nil {
			b.cfg.Logger.Warn("impact context stage failed", "error", err)
		}
		results.impactDocs = filterTestDocs(docs)
	})

	if prDescription != "" {
		wg.Go(func() {
			docs, err := b.gatherDescriptionDocs(ctx, collectionName, embedderModelName, prDescription)
			if err != nil {
				b.cfg.Logger.Warn("description context stage failed", "error", err)
			}
			results.descriptionDocs = filterTestDocs(docs)
		})
	}

	wg.Go(func() {
		defs, err := b.gatherDefinitionsContext(ctx, scopedStore, changedFiles)
		if err != nil {
			b.cfg.Logger.Warn("definitions context stage failed", "error", err)
		}
		results.definitionsContext = defs
	})

	wg.Go(func() {
		pkg, err := b.gatherPackageContextSafe(ctx, scopedStore, changedFiles)
		if err != nil {
			b.cfg.Logger.Warn("package context stage failed", "error", err)
		}
		results.packageContext = pkg
	})

	wg.Go(func() {
		rel, err := b.gatherRelationsContextSafe(ctx, scopedStore, changedFiles)
		if err != nil {
			b.cfg.Logger.Warn("relations context stage failed", "error", err)
		}
		results.relationContext = rel
	})

	wg.Wait()

	// Gather test coverage context after definitions (depends on extracted symbols)
	if len(results.definitionsContext) > 0 {
		docs, err := b.gatherTestCoverageContext(ctx, scopedStore, changedFiles, results.definitionsContext)
		if err != nil {
			b.cfg.Logger.Warn("test coverage context failed", "error", err)
		} else {
			results.testCoverageDocs = docs
		}
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

func (b *builderImpl) assembleContext(ctx context.Context, arch, toc, fileSummary, impact, description, definitions, testCoverage, pkgContext, relContext string, hyde [][]schema.Document, indices []int, files []internalgithub.ChangedFile) string {
	docs := b.buildContextDocuments(arch, toc, fileSummary, impact, description, definitions, testCoverage, hyde, indices, files)

	// Prepend package and relations context to the docs
	if pkgContext != "" || relContext != "" {
		var contextSections []string
		if pkgContext != "" {
			contextSections = append(contextSections, "## Package Summary\n\n"+pkgContext)
		}
		if relContext != "" {
			contextSections = append(contextSections, "## Cross-File Dependencies\n\n"+relContext)
		}
		prependDoc := schema.NewDocument(strings.Join(contextSections, "\n"), map[string]any{
			"chunk_type": "context_summary",
			"source":     "__generated__",
		})
		docs = append([]schema.Document{prependDoc}, docs...)
	}

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
		"file_summary_len", len(fileSummary),
		"impact_len", len(impact),
		"definitions_len", len(definitions),
		"hyde_results_count", len(hyde),
		"package_len", len(pkgContext),
		"relations_len", len(relContext),
		"total_tokens", result.TokenStats.TotalTokens,
		"documents_packed", result.TokenStats.DocumentsPacked,
		"documents_considered", result.TokenStats.DocumentsConsidered,
		"truncated", result.Truncated,
	)

	return result.Content
}

// hashPatch returns a 128-bit hex hash of the patch content for cache keying.
func (b *builderImpl) hashPatch(patch string) string {
	return cryptoutil.HashStringShort(patch)
}
