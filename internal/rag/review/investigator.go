package review

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/sevigo/goframe/llms"
	"github.com/sevigo/goframe/vectorstores"

	"github.com/sevigo/code-warden/internal/llm"
	"github.com/sevigo/code-warden/internal/storage"
)

const (
	maxGapCalls    = 15
	defaultGapArgs = 5
	// Truncation limits to keep the gap-identification prompt within fast-model context windows.
	maxDiffChars        = 8000
	maxContextChars     = 4000
	maxDefinitionsChars = 3000
)

// GapQuery represents a single tool invocation chosen by the gap-finding LLM.
type GapQuery struct {
	Tool   string         `json:"tool"`
	Reason string         `json:"reason"`
	Args   map[string]any `json:"args"`
}

type gapIdentificationOutput struct {
	Gaps []GapQuery `json:"gaps"`
}

// Investigator performs Phase 2 of the agentic review: gap-filling context retrieval.
//
// Phase 1 (BuildContext) retrieves context via a fixed set of parallel RAG stages.
// Phase 2 (Investigator.Investigate) uses a fast LLM to identify what additional
// context would improve review quality, then executes targeted vector store queries
// to fill those gaps. Up to [maxGapCalls] tool calls are executed.
//
// All errors are logged and suppressed — this stage must never block the review.
type Investigator struct {
	vectorStore storage.VectorStore
	promptMgr   *llm.PromptManager
	embedder    string
	fastModel   string
	getLLM      LLMFactory
	logger      *slog.Logger
}

// NewInvestigator creates a new [Investigator] for Phase 2 gap-filling.
func NewInvestigator(
	vs storage.VectorStore,
	promptMgr *llm.PromptManager,
	embedder, fastModel string,
	getLLM LLMFactory,
	logger *slog.Logger,
) *Investigator {
	return &Investigator{
		vectorStore: vs,
		promptMgr:   promptMgr,
		embedder:    embedder,
		fastModel:   fastModel,
		getLLM:      getLLM,
		logger:      logger,
	}
}

// Investigate is the InvestigateFunc implementation. It identifies context gaps and
// retrieves additional context via targeted queries. Returns an empty string on error
// or when no gaps are found.
func (inv *Investigator) Investigate(
	ctx context.Context,
	collectionName, diff, mainContext, definitionsContext string,
) string {
	inv.logger.Info("phase 2 started", "collection", collectionName)

	fastLLM, err := inv.getLLM(ctx, inv.fastModel)
	if err != nil {
		inv.logger.Warn("phase 2 skipped: failed to get fast LLM", "model", inv.fastModel, "error", err)
		return ""
	}

	scopedStore := inv.vectorStore.ForRepo(collectionName, inv.embedder)

	gaps, err := inv.identifyGaps(ctx, fastLLM, diff, mainContext, definitionsContext)
	if err != nil {
		inv.logger.Warn("phase 2 skipped: gap identification failed", "error", err)
		return ""
	}
	if len(gaps) == 0 {
		inv.logger.Info("phase 2 completed: no gaps identified")
		return ""
	}

	if len(gaps) > maxGapCalls {
		gaps = gaps[:maxGapCalls]
	}

	inv.logger.Info("phase 2 executing gap queries", "count", len(gaps))

	var sections []string
	for i, gap := range gaps {
		result := inv.executeGap(ctx, scopedStore, gap)
		if result != "" {
			sections = append(sections, fmt.Sprintf("### Gap %d: %s\n%s", i+1, gap.Reason, result))
		}
	}

	if len(sections) == 0 {
		inv.logger.Info("phase 2 completed: queries returned no results")
		return ""
	}

	inv.logger.Info("phase 2 completed", "gaps_filled", len(sections))
	return "# Additional Context (Phase 2 Investigation)\n\n" + strings.Join(sections, "\n\n")
}

func (inv *Investigator) identifyGaps(
	ctx context.Context,
	fastLLM llms.Model,
	diff, mainContext, definitionsContext string,
) ([]GapQuery, error) {
	promptData := map[string]any{
		"Diff":        truncateStr(diff, maxDiffChars),
		"Context":     truncateStr(mainContext, maxContextChars),
		"Definitions": truncateStr(definitionsContext, maxDefinitionsChars),
		"MaxGaps":     maxGapCalls,
	}

	prompt, err := inv.promptMgr.Render(llm.GapIdentificationPrompt, promptData)
	if err != nil {
		return nil, fmt.Errorf("failed to render gap identification prompt: %w", err)
	}

	response, err := fastLLM.Call(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("fast LLM call failed: %w", err)
	}

	return parseGapOutput(response)
}

func parseGapOutput(response string) ([]GapQuery, error) {
	// Strip markdown code fences if the model wraps its JSON output.
	response = strings.TrimSpace(response)
	if strings.HasPrefix(response, "```") {
		if idx := strings.Index(response, "\n"); idx >= 0 {
			response = response[idx+1:]
		}
		if idx := strings.LastIndex(response, "```"); idx >= 0 {
			response = response[:idx]
		}
		response = strings.TrimSpace(response)
	}

	var output gapIdentificationOutput
	if err := json.Unmarshal([]byte(response), &output); err != nil {
		return nil, fmt.Errorf("failed to parse gap output: %w", err)
	}
	return output.Gaps, nil
}

func (inv *Investigator) executeGap(ctx context.Context, store storage.ScopedVectorStore, gap GapQuery) string {
	switch gap.Tool {
	case "search_code":
		return inv.executeSearchCode(ctx, store, gap.Args)
	case "get_symbol":
		return inv.executeGetSymbol(ctx, store, gap.Args)
	case "find_usages":
		return inv.executeFindUsages(ctx, store, gap.Args)
	default:
		inv.logger.Warn("phase 2: unknown tool requested", "tool", gap.Tool)
		return ""
	}
}

func (inv *Investigator) executeSearchCode(ctx context.Context, store storage.ScopedVectorStore, args map[string]any) string {
	query, ok := args["query"].(string)
	if !ok || query == "" {
		return ""
	}
	limit := parseIntArg(args, "limit", defaultGapArgs)

	opts := []vectorstores.Option{}
	if ct, ok := args["chunk_type"].(string); ok && ct != "" {
		opts = append(opts, vectorstores.WithFilters(map[string]any{"chunk_type": ct}))
	}

	docs, err := store.SimilaritySearchWithScores(ctx, query, limit, opts...)
	if err != nil {
		inv.logger.Debug("phase 2 search_code failed", "query", query, "error", err)
		return ""
	}

	var sb strings.Builder
	for _, ds := range docs {
		source, _ := ds.Document.Metadata["source"].(string)
		fmt.Fprintf(&sb, "**%s**\n```\n%s\n```\n", source, ds.Document.PageContent)
	}
	return sb.String()
}

func (inv *Investigator) executeGetSymbol(ctx context.Context, store storage.ScopedVectorStore, args map[string]any) string {
	name, ok := args["name"].(string)
	if !ok || name == "" {
		return ""
	}

	// Try exact match first, then fall back to semantic search.
	docs, err := store.SimilaritySearch(ctx, "definition of "+name, 3,
		vectorstores.WithFilters(map[string]any{
			"chunk_type": "definition",
			"identifier": name,
		}),
	)
	if err != nil || len(docs) == 0 {
		docs, err = store.SimilaritySearch(ctx, "definition of "+name, 1,
			vectorstores.WithFilters(map[string]any{"chunk_type": "definition"}),
		)
		if err != nil || len(docs) == 0 {
			inv.logger.Debug("phase 2 get_symbol: not found", "name", name)
			return ""
		}
	}

	doc := docs[0]
	source, _ := doc.Metadata["source"].(string)
	return fmt.Sprintf("**Definition of `%s`** (from %s)\n```\n%s\n```\n", name, source, doc.PageContent)
}

func (inv *Investigator) executeFindUsages(ctx context.Context, store storage.ScopedVectorStore, args map[string]any) string {
	symbol, ok := args["symbol"].(string)
	if !ok || symbol == "" {
		return ""
	}
	limit := parseIntArg(args, "limit", defaultGapArgs)

	query := symbol + " usage call reference"
	docs, err := store.SimilaritySearchWithScores(ctx, query, limit*2,
		vectorstores.WithFilters(map[string]any{
			"chunk_type": map[string]any{"$ne": "definition"},
		}),
	)
	if err != nil {
		inv.logger.Debug("phase 2 find_usages failed", "symbol", symbol, "error", err)
		return ""
	}

	var sb strings.Builder
	count := 0
	for _, ds := range docs {
		if !strings.Contains(ds.Document.PageContent, symbol) {
			continue
		}
		source, _ := ds.Document.Metadata["source"].(string)
		fmt.Fprintf(&sb, "**%s**\n```\n%s\n```\n", source, ds.Document.PageContent)
		count++
		if count >= limit {
			break
		}
	}
	return sb.String()
}

func parseIntArg(args map[string]any, key string, defaultVal int) int {
	switch v := args[key].(type) {
	case float64:
		if n := int(v); n > 0 {
			return n
		}
	case int:
		if v > 0 {
			return v
		}
	}
	return defaultVal
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "\n...[truncated]"
}
