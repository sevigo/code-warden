package detect

import (
	"context"
	"strconv"
	"strings"

	"github.com/sevigo/goframe/llms"
	"github.com/sevigo/goframe/output"

	"github.com/sevigo/code-warden/internal/llm"
)

// SnippetValidator validates code snippet relevance to a PR via batch LLM calls.
type SnippetValidator struct {
	validatorLLM llms.Model
	promptMgr    *llm.PromptManager
}

// NewSnippetValidator creates a new [SnippetValidator].
func NewSnippetValidator(validatorLLM llms.Model, promptMgr *llm.PromptManager) *SnippetValidator {
	return &SnippetValidator{
		validatorLLM: validatorLLM,
		promptMgr:    promptMgr,
	}
}

// batchValidationResult maps snippet index (as string) to relevance boolean.
type batchValidationResult map[string]bool

// ValidateBatch validates all snippets in a single LLM call, returning relevance per index.
func (v *SnippetValidator) ValidateBatch(ctx context.Context, snippets []string, prContext string) map[int]bool {
	result := make(map[int]bool, len(snippets))
	for i := range snippets {
		result[i] = true // fail-open default
	}

	if v.validatorLLM == nil || len(snippets) == 0 {
		return result
	}

	prompt, err := v.buildBatchPrompt(snippets, prContext)
	if err != nil {
		return result
	}

	raw, llmErr := llms.GenerateFromSinglePrompt(ctx, v.validatorLLM, prompt)
	if llmErr != nil {
		return result
	}

	parser := output.NewJSONParser[batchValidationResult]()
	parsed, parseErr := parser.Parse(ctx, raw)
	if parseErr != nil {
		return result
	}

	applyParsedResults(parsed, len(snippets), result)
	return result
}

// buildBatchPrompt constructs the prompt for batch snippet validation.
func (v *SnippetValidator) buildBatchPrompt(snippets []string, prContext string) (string, error) {
	var snippetList strings.Builder
	for i, s := range snippets {
		preview := s
		if len(preview) > 500 {
			preview = preview[:500] + "..."
		}
		snippetList.WriteString("--- Snippet ")
		snippetList.WriteString(strconv.Itoa(i))
		snippetList.WriteString(" ---\n")
		snippetList.WriteString(preview)
		snippetList.WriteString("\n\n")
	}

	return v.promptMgr.Render(llm.ValidateSnippetsBatchPrompt, map[string]string{
		"context":  prContext,
		"snippets": snippetList.String(),
		"count":    strconv.Itoa(len(snippets)),
	})
}

// applyParsedResults maps LLM validation results back to the result map by index.
func applyParsedResults(parsed batchValidationResult, snippetCount int, result map[int]bool) {
	for k, relevant := range parsed {
		idx, err := strconv.Atoi(strings.TrimSpace(k))
		if err == nil && idx >= 0 && idx < snippetCount {
			result[idx] = relevant
		}
	}
}
