package rag

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/sevigo/goframe/chains"
	"github.com/sevigo/goframe/llms"
	"github.com/sevigo/goframe/prompts"

	"github.com/sevigo/code-warden/internal/llm"
)

// snippetValidator uses goframe's LLMChain pattern to validate
// if a code snippet is relevant to a given context/query.
type snippetValidator struct {
	validatorLLM llms.Model
	promptMgr    *llm.PromptManager
}

// validateSnippetResult is the parsed result from the validation LLM.
type validateSnippetResult struct {
	Relevant bool   `json:"relevant"`
	Reason   string `json:"reason"`
}

// snippetOutputParser implements output parsing for validation results.
type snippetOutputParser struct{}

func (p *snippetOutputParser) Parse(_ context.Context, output string) (*validateSnippetResult, error) {
	// Try to extract JSON from the response
	start := strings.Index(output, "{")
	end := strings.LastIndex(output, "}")
	if start == -1 || end == -1 || end < start {
		// Fallback: check for YES/NO pattern
		if strings.Contains(strings.ToUpper(output), "YES") {
			return &validateSnippetResult{Relevant: true, Reason: "positive response detected"}, nil
		}
		return &validateSnippetResult{Relevant: false, Reason: "no valid response"}, nil
	}

	jsonStr := output[start : end+1]
	var result validateSnippetResult
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		// Log parse error but return result with reason (intentional fail-open behavior)
		//nolint:nilerr // Intentional: fail-open design, error is captured in Reason field
		return &validateSnippetResult{Relevant: false, Reason: "parse error: " + err.Error()}, nil
	}
	return &result, nil
}

// newSnippetValidator creates a new snippet validator using the provided LLM.
func newSnippetValidator(validatorLLM llms.Model, promptMgr *llm.PromptManager) *snippetValidator {
	return &snippetValidator{
		validatorLLM: validatorLLM,
		promptMgr:    promptMgr,
	}
}

// validate checks if a snippet is relevant to the given context.
// Returns true if relevant, false otherwise. Fails open (returns true) on errors.
func (v *snippetValidator) validate(ctx context.Context, snippet, context string) bool {
	if v.validatorLLM == nil {
		return true // Fail open: if no validator available, include the snippet
	}

	promptData := map[string]string{
		"context": context,
		"snippet": snippet,
	}

	prompt, err := v.promptMgr.Render(llm.ValidateSnippetPrompt, promptData)
	if err != nil {
		// Fail open on prompt rendering errors
		return true
	}

	parser := &snippetOutputParser{}
	chain := chains.NewLLMChain(
		v.validatorLLM,
		prompts.NewPromptTemplate(prompt),
		chains.WithOutputParser(parser),
	)

	result, err := chain.Call(ctx, nil)
	if err != nil {
		// Fail open on LLM errors
		return true
	}

	return result.Relevant
}

// batchValidationResult is the parsed JSON result from a batch validation call.
// The LLM returns a map of string-index to bool relevance, e.g. {"0": true, "1": false}.
type batchValidationResult map[string]bool

// validateBatch sends all snippets to the LLM in a single call and returns a map
// of snippet index → relevant. Fails open (all true) on any error.
// This is Issue #6's fix: replaces N sequential LLM calls with one batched call.
func (v *snippetValidator) validateBatch(ctx context.Context, snippets []string, prContext string) map[int]bool {
	result := make(map[int]bool, len(snippets))
	// Default: fail open — include all snippets
	for i := range snippets {
		result[i] = true
	}

	if v.validatorLLM == nil || len(snippets) == 0 {
		return result
	}

	// Build a numbered list of snippets for the LLM
	var snippetList strings.Builder
	for i, s := range snippets {
		// Truncate long snippets to keep the prompt manageable
		preview := s
		if len(preview) > 500 {
			preview = preview[:500] + "..."
		}
		snippetList.WriteString("--- Snippet ")
		snippetList.WriteString(strings.TrimSpace(strings.Repeat(" ", 0)))
		snippetList.WriteString(itoa(i))
		snippetList.WriteString(" ---\n")
		snippetList.WriteString(preview)
		snippetList.WriteString("\n\n")
	}

	promptData := map[string]string{
		"context":  prContext,
		"snippets": snippetList.String(),
		"count":    itoa(len(snippets)),
	}

	prompt, err := v.promptMgr.Render(llm.ValidateSnippetsBatchPrompt, promptData)
	if err != nil {
		// Fail open: prompt template not found or render error
		return result
	}

	// Use GenerateFromSinglePrompt to get a raw text response — no output parser needed,
	// we parse the JSON ourselves. This also avoids the generic type inference issue with
	// chains.NewLLMChain when no typed output parser is provided.
	raw, llmErr := llms.GenerateFromSinglePrompt(ctx, v.validatorLLM, prompt)
	if llmErr != nil {
		return result // Fail open
	}

	// Parse JSON response: expects {"0": true, "1": false, ...}
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start == -1 || end == -1 || end < start {
		return result // Fail open: malformed response
	}

	var parsed batchValidationResult
	if err := json.Unmarshal([]byte(raw[start:end+1]), &parsed); err != nil {
		return result // Fail open: JSON parse error
	}

	// Apply parsed results — only override where we got a definitive answer
	for k, v := range parsed {
		idx := 0
		fmt := strings.TrimSpace(k)
		for _, c := range fmt {
			if c >= '0' && c <= '9' {
				idx = idx*10 + int(c-'0')
			}
		}
		if idx >= 0 && idx < len(snippets) {
			result[idx] = v
		}
	}

	return result
}

// itoa converts an int to string without importing strconv.
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
