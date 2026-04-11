package llm

import (
	"bytes"
	"embed"
	"fmt"
	"path/filepath"
	"strings"
	"text/template"
)

//go:embed prompts/*.prompt
var promptFiles embed.FS

type PromptKey string

const (
	CodeReviewPrompt            PromptKey = "code_review"
	CodeGenerationPrompt        PromptKey = "code_generation"
	ReReviewPrompt              PromptKey = "rereview"
	ArchSummaryPrompt           PromptKey = "arch_summary"
	FileSummaryPrompt           PromptKey = "file_summary"
	QuestionPrompt              PromptKey = "question"
	HyDEPrompt                  PromptKey = "hyde_code"
	ConsensusReviewPrompt       PromptKey = "consensus_review"
	ValidateSnippetPrompt       PromptKey = "validate_snippet"
	ValidateSnippetsBatchPrompt PromptKey = "validate_snippets_batch"
	IntentExtractionPrompt      PromptKey = "intent_extraction"
	ReuseVerificationPrompt     PromptKey = "reuse_verification"
	ProjectContextPrompt        PromptKey = "project_context"
	GapIdentificationPrompt     PromptKey = "gap_identification"
)

type PromptManager struct {
	prompts map[PromptKey]*template.Template
	raw     map[PromptKey]string
}

func NewPromptManager() (*PromptManager, error) {
	pm := &PromptManager{
		prompts: make(map[PromptKey]*template.Template),
		raw:     make(map[PromptKey]string),
	}

	files, err := promptFiles.ReadDir("prompts")
	if err != nil {
		return nil, fmt.Errorf("failed to read embedded prompts directory: %w", err)
	}

	for _, file := range files {
		if file.IsDir() {
			continue
		}

		fileName := file.Name()
		key := PromptKey(strings.TrimSuffix(fileName, filepath.Ext(fileName)))

		content, err := promptFiles.ReadFile("prompts/" + fileName)
		if err != nil {
			return nil, fmt.Errorf("failed to read embedded prompt file %s: %w", fileName, err)
		}

		tmpl, err := template.New(string(key)).Parse(string(content))
		if err != nil {
			return nil, fmt.Errorf("could not parse template from file %s: %w", fileName, err)
		}

		pm.prompts[key] = tmpl
		pm.raw[key] = string(content)
	}

	return pm, nil
}

func (pm *PromptManager) Get(key PromptKey) (*template.Template, error) {
	tmpl, ok := pm.prompts[key]
	if !ok {
		return nil, fmt.Errorf("no prompt found for key '%s'", key)
	}
	return tmpl, nil
}

func (pm *PromptManager) Raw(key PromptKey) (string, error) {
	s, ok := pm.raw[key]
	if !ok {
		return "", fmt.Errorf("no prompt found for key '%s'", key)
	}
	return s, nil
}

func (pm *PromptManager) Render(key PromptKey, data any) (string, error) {
	tmpl, err := pm.Get(key)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to render template: %w", err)
	}

	return buf.String(), nil
}
