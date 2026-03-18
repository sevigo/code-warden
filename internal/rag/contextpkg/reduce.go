package contextpkg

import (
	"context"
	"fmt"
	"strings"

	"github.com/sevigo/goframe/chains"
	"github.com/sevigo/goframe/llms"
	"github.com/sevigo/goframe/schema"
	"github.com/sevigo/goframe/vectorstores"

	"github.com/sevigo/code-warden/internal/llm"
	"github.com/sevigo/code-warden/internal/storage"
)

// GenerateProjectContext fetches all directory-level architectural summaries
// and synthesizes them into a global project context document.
func (b *builderImpl) GenerateProjectContext(ctx context.Context, collectionName, embedderModelName string) (string, error) {
	b.cfg.Logger.Info("generating project context document from arch summaries",
		"collection", collectionName,
	)

	scopedStore := b.cfg.VectorStore.ForRepo(collectionName, embedderModelName)
	chain, err := chains.NewDocumentMapReduceChain(
		b.createArchSummaryMapFunc(scopedStore),
		b.createProjectContextReduceFunc(),
	)
	if err != nil {
		return "", fmt.Errorf("failed to initialize context generation pipeline: %w", err)
	}

	result, err := chain.Execute(ctx, nil)
	if err != nil {
		return b.handleEmptyDocumentsError(err)
	}
	return result, nil
}

// createArchSummaryMapFunc returns a function that fetches architectural summaries from the vector store.
func (b *builderImpl) createArchSummaryMapFunc(scopedStore storage.ScopedVectorStore) func(ctx context.Context, _ any) ([]schema.Document, error) {
	return func(ctx context.Context, _ any) ([]schema.Document, error) {
		limit := b.cfg.AIConfig.MaxContextSummaries
		if limit <= 0 {
			limit = 1000
		}

		docs, err := scopedStore.SimilaritySearch(ctx, "summary", limit,
			vectorstores.WithFilters(map[string]any{
				"chunk_type": "arch",
			}),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch architectural summaries: %w", err)
		}

		if len(docs) == 0 {
			b.cfg.Logger.Warn("no architectural summaries found to generate context from")
			return nil, nil
		}

		return docs, nil
	}
}

// createProjectContextReduceFunc returns a function that synthesizes documents into project context.
func (b *builderImpl) createProjectContextReduceFunc() func(ctx context.Context, docs []schema.Document) (string, error) {
	return func(ctx context.Context, docs []schema.Document) (string, error) {
		if len(docs) == 0 {
			return "", nil
		}

		combinedSummaries := b.combineArchSummaries(docs)

		prompt, err := b.cfg.PromptMgr.Render(llm.ProjectContextPrompt, map[string]string{
			"Summaries": combinedSummaries,
		})
		if err != nil {
			return "", fmt.Errorf("failed to render project context prompt: %w", err)
		}

		response, err := llms.GenerateFromSinglePrompt(ctx, b.cfg.GeneratorLLM, prompt)
		if err != nil {
			return "", fmt.Errorf("failed to generate project context: %w", err)
		}

		b.cfg.Logger.Info("project context document generated successfully",
			"incoming_summaries", len(docs),
			"output_length", len(response),
		)

		return response, nil
	}
}

// combineArchSummaries combines architectural summary documents into a single string.
func (b *builderImpl) combineArchSummaries(docs []schema.Document) string {
	var combined strings.Builder
	for _, doc := range docs {
		source, _ := doc.Metadata["source"].(string)
		if source == "" {
			source = "unknown directory"
		}
		fmt.Fprintf(&combined, "## Directory: %s\n%s\n\n", source, doc.PageContent)
	}
	return combined.String()
}

// handleEmptyDocumentsError handles the empty documents error gracefully.
func (b *builderImpl) handleEmptyDocumentsError(err error) (string, error) {
	if strings.Contains(err.Error(), "mapper returned no documents") {
		b.cfg.Logger.Warn("no architectural summaries found to generate context from")
		return "", nil
	}
	return "", fmt.Errorf("context generation failed: %w", err)
}
