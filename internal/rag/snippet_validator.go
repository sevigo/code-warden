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

func (p *snippetOutputParser) Parse(ctx context.Context, output string) (*validateSnippetResult, error) {
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
	chain := chains.NewLLMChain[*validateSnippetResult](
		v.validatorLLM,
		prompts.NewPromptTemplate(prompt),
		chains.WithOutputParser[*validateSnippetResult](parser),
	)

	result, err := chain.Call(ctx, nil)
	if err != nil {
		// Fail open on LLM errors
		return true
	}

	return result.Relevant
}
