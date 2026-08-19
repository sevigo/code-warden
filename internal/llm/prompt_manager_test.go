package llm

import (
	"strings"
	"testing"
)

func TestPromptManager_Raw(t *testing.T) {
	pm, err := NewPromptManager()
	if err != nil {
		t.Fatalf("NewPromptManager() error = %v", err)
	}

	raw, err := pm.Raw("review_bug")
	if err != nil {
		t.Fatalf("Raw(review_bug) error = %v", err)
	}

	if !strings.Contains(raw, "investigation") {
		t.Error("Raw() should contain the review_bug prompt content, got unexpected output")
	}
}

func TestPromptManager_Raw_NotFound(t *testing.T) {
	pm, err := NewPromptManager()
	if err != nil {
		t.Fatalf("NewPromptManager() error = %v", err)
	}

	_, err = pm.Raw("nonexistent_prompt")
	if err == nil {
		t.Error("expected error for nonexistent prompt key")
	}
}

// TestPromptManager_Render_NoVars renders a prompt without template variables
// (the review_* prompts have none) and confirms Raw() and Render() agree.
func TestPromptManager_Render_NoVars(t *testing.T) {
	pm, err := NewPromptManager()
	if err != nil {
		t.Fatalf("NewPromptManager() error = %v", err)
	}

	rendered, err := pm.Render("review_bug", nil)
	if err != nil {
		t.Fatalf("Render(review_bug, nil) error = %v", err)
	}

	raw, _ := pm.Raw("review_bug")
	if rendered != raw {
		t.Error("Render(nil) of a template with no variables should equal Raw()")
	}
}
