package contextpkg

import (
	"context"
	"fmt"
	"strings"

	"github.com/sevigo/goframe/llms"
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

	limit := b.cfg.AIConfig.MaxContextSummaries
	if limit <= 0 {
		limit = 1000
	}

	// 1. Fetch all architectural summaries for the repository
	// Using a generic search "summary" with a high limit to get all of them
	docs, err := scopedStore.SimilaritySearch(ctx, "summary", limit,
		vectorstores.WithFilters(map[string]any{
			"chunk_type": "arch",
		}),
	)
	if err != nil {
		return "", fmt.Errorf("failed to fetch architectural summaries: %w", err)
	}

	if len(docs) == 0 {
		b.cfg.Logger.Warn("no architectural summaries found to generate context from")
		return "", nil // Return empty, nothing to summarize
	}

	// 2. Combine them into a single string for the prompt
	var combinedSummaries strings.Builder
	for _, doc := range docs {
		source, _ := doc.Metadata["source"].(string)
		if source == "" {
			source = "unknown directory"
		}
		combinedSummaries.WriteString(fmt.Sprintf("## Directory: %s\n%s\n\n", source, doc.PageContent))
	}

	// 3. Render the prompt
	promptData := map[string]string{
		"Summaries": combinedSummaries.String(),
	}

	prompt, err := b.cfg.PromptMgr.Render(llm.ProjectContextPrompt, promptData)
	if err != nil {
		return "", fmt.Errorf("failed to render project context prompt: %w", err)
	}

	// 4. Call Generator LLM (the REDUCE step)
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
