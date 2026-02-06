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

type ModelProvider string
type PromptKey string

const (
	DefaultProvider       ModelProvider = "default"
	CodeReviewPrompt      PromptKey     = "code_review"
	CodeGenerationPrompt  PromptKey     = "code_generation"
	ReReviewPrompt        PromptKey     = "rereview"
	ArchSummaryPrompt     PromptKey     = "arch_summary"
	QuestionPrompt        PromptKey     = "question"
	HyDEPrompt            PromptKey     = "hyde_code"
	ConsensusReviewPrompt PromptKey     = "consensus_review"
)

type PromptManager struct {
	prompts map[PromptKey]map[ModelProvider]*template.Template
}

func NewPromptManager() (*PromptManager, error) {
	pm := &PromptManager{
		prompts: make(map[PromptKey]map[ModelProvider]*template.Template),
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
		baseName := strings.TrimSuffix(fileName, filepath.Ext(fileName))
		lastUnderscore := strings.LastIndex(baseName, "_")
		if lastUnderscore == -1 || lastUnderscore == 0 || lastUnderscore == len(baseName)-1 {
			return nil, fmt.Errorf("invalid prompt filename format: %s (expected 'key_provider.prompt' with non-empty key and provider)", fileName)
		}

		key := PromptKey(baseName[:lastUnderscore])
		provider := ModelProvider(baseName[lastUnderscore+1:])

		content, err := promptFiles.ReadFile("prompts/" + fileName)
		if err != nil {
			return nil, fmt.Errorf("failed to read embedded prompt file %s: %w", fileName, err)
		}

		if err := pm.register(key, provider, string(content)); err != nil {
			return nil, fmt.Errorf("failed to register prompt from file %s: %w", fileName, err)
		}
	}

	return pm, nil
}

func (pm *PromptManager) register(key PromptKey, provider ModelProvider, content string) error {
	tmpl, err := template.New(string(key) + "_" + string(provider)).Parse(content)
	if err != nil {
		return fmt.Errorf("could not parse template: %w", err)
	}

	if _, ok := pm.prompts[key]; !ok {
		pm.prompts[key] = make(map[ModelProvider]*template.Template)
	}

	pm.prompts[key][provider] = tmpl
	return nil
}

func (pm *PromptManager) Get(key PromptKey, provider ModelProvider) (*template.Template, error) {
	taskPrompts, ok := pm.prompts[key]
	if !ok {
		return nil, fmt.Errorf("no prompts found for key '%s'", key)
	}

	if tmpl, ok := taskPrompts[provider]; ok {
		return tmpl, nil
	}
	if tmpl, ok := taskPrompts[DefaultProvider]; ok {
		return tmpl, nil
	}

	return nil, fmt.Errorf("no template found for key '%s' and provider '%s', and no default was available", key, provider)
}

func (pm *PromptManager) Render(key PromptKey, provider ModelProvider, data any) (string, error) {
	tmpl, err := pm.Get(key, provider)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to render template: %w", err)
	}

	return buf.String(), nil
}
