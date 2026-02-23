package rag

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/sevigo/goframe/llms"

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

	parsed, parseErr := parseBatchResponse(raw)
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
		snippetList.WriteString(itoa(i))
		snippetList.WriteString(" ---\n")
		snippetList.WriteString(preview)
		snippetList.WriteString("\n\n")
	}

	return v.promptMgr.Render(llm.ValidateSnippetsBatchPrompt, map[string]string{
		"context":  prContext,
		"snippets": snippetList.String(),
		"count":    itoa(len(snippets)),
	})
}

// parseBatchResponse extracts JSON from the LLM's raw response.
func parseBatchResponse(raw string) (batchValidationResult, error) {
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start == -1 || end == -1 || end < start {
		return nil, errNoJSON
	}
	var parsed batchValidationResult
	if err := json.Unmarshal([]byte(raw[start:end+1]), &parsed); err != nil {
		return nil, err
	}
	return parsed, nil
}

// applyParsedResults applies the relevance decisions to the result map.
func applyParsedResults(parsed batchValidationResult, snippetCount int, result map[int]bool) {
	for k, relevant := range parsed {
		idx := atoiSafe(k)
		if idx >= 0 && idx < snippetCount {
			result[idx] = relevant
		}
	}
}

// errNoJSON is returned when the LLM response contains no JSON object.
var errNoJSON = errors.New("no JSON object found in response")

// itoa converts a non-negative int to string without importing strconv.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}

// atoiSafe converts a decimal string to int, returning -1 on any invalid input.
func atoiSafe(s string) int {
	s = strings.TrimSpace(s)
	if len(s) == 0 {
		return -1
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return -1
		}
		n = n*10 + int(c-'0')
	}
	return n
}
