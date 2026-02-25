package rag

import (
	"context"
	"strconv"
	"strings"

	"github.com/sevigo/goframe/llms"
	"github.com/sevigo/goframe/output"

	"github.com/sevigo/code-warden/internal/llm"
)

// snippetValidator validates whether code snippets are relevant to a PR description.
type snippetValidator struct {
	validatorLLM llms.Model
	promptMgr    *llm.PromptManager
}

// newSnippetValidator creates a new validator.
func newSnippetValidator(validatorLLM llms.Model, promptMgr *llm.PromptManager) *snippetValidator {
	return &snippetValidator{
		validatorLLM: validatorLLM,
		promptMgr:    promptMgr,
	}
}

// batchValidationResult is the parsed JSON result from a batch validation call:
// {"0": true, "1": false, ...}
type batchValidationResult map[string]bool

// validateBatch validates all snippets in a single call.
func (v *snippetValidator) validateBatch(ctx context.Context, snippets []string, prContext string) map[int]bool {
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

// buildBatchPrompt constructs the prompt for batch validation.
func (v *snippetValidator) buildBatchPrompt(snippets []string, prContext string) (string, error) {
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

// applyParsedResults applies the relevance decisions to the result map.
func applyParsedResults(parsed batchValidationResult, snippetCount int, result map[int]bool) {
	for k, relevant := range parsed {
		idx, err := strconv.Atoi(strings.TrimSpace(k))
		if err == nil && idx >= 0 && idx < snippetCount {
			result[idx] = relevant
		}
	}
}
