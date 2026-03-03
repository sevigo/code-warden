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
)

// GenerateProjectContext fetches all directory-level architectural summaries
// and synthesizes them into a global project context document.
func (b *builderImpl) GenerateProjectContext(ctx context.Context, collectionName, embedderModelName string) (string, error) {
	b.cfg.Logger.Info("generating project context document from arch summaries",
		"collection", collectionName,
	)

	scopedStore := b.cfg.VectorStore.ForRepo(collectionName, embedderModelName)

	// Define MAP function: fetches architectural summaries from vector store
	mapFunc := func(ctx context.Context, input any) ([]schema.Document, error) {
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
			return nil, nil // Return nil to signal no docs - chain will handle error
		}

		return docs, nil
	}

	// Define REDUCE function: synthesizes documents into project context
	reduceFunc := func(ctx context.Context, docs []schema.Document) (string, error) {
		if len(docs) == 0 {
			return "", nil
		}

		// Combine documents into a single string for the prompt
		var combinedSummaries strings.Builder
		for _, doc := range docs {
			source, _ := doc.Metadata["source"].(string)
			if source == "" {
				source = "unknown directory"
			}
			combinedSummaries.WriteString(fmt.Sprintf("## Directory: %s\n%s\n\n", source, doc.PageContent))
		}

		promptData := map[string]string{
			"Summaries": combinedSummaries.String(),
		}

		prompt, err := b.cfg.PromptMgr.Render(llm.ProjectContextPrompt, promptData)
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

	chain, err := chains.NewDocumentMapReduceChain(mapFunc, reduceFunc)
	if err != nil {
		return "", fmt.Errorf("failed to initialize context generation pipeline: %w", err)
	}

	// Execute the chain - input is ignored in this case since mapFunc handles fetching
	result, err := chain.Execute(ctx, nil)
	if err != nil {
		// Handle empty documents gracefully - return empty string, not error
		// This maintains backward compatibility with the original behavior
		if strings.Contains(err.Error(), "mapper returned no documents") {
			b.cfg.Logger.Warn("no architectural summaries found to generate context from")
			return "", nil
		}
		return "", fmt.Errorf("context generation failed: %w", err)
	}

	return result, nil
}
