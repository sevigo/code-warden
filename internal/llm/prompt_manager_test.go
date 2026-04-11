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

	raw, err := pm.Raw("rerank_precision")
	if err != nil {
		t.Fatalf("Raw(rerank_precision) error = %v", err)
	}

	if !strings.Contains(raw, "{{.Query}}") {
		t.Error("Raw() should contain {{.Query}} template variable, got rendered output without it")
	}
	if !strings.Contains(raw, "{{.Source}}") {
		t.Error("Raw() should contain {{.Source}} template variable")
	}
	if !strings.Contains(raw, "{{.Content}}") {
		t.Error("Raw() should contain {{.Content}} template variable")
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

// TestPromptManager_Render_WithNilProducesNoValue verifies that
// Render(key, nil) replaces template variables with "<no value>". This is
// intentional — templates that need runtime data must use Raw() instead.
// This test documents the behavior to prevent accidental misuse of Render
// for templates consumed by external renderers (e.g. goframe reranker).
func TestPromptManager_Render_WithNilProducesNoValue(t *testing.T) {
	pm, err := NewPromptManager()
	if err != nil {
		t.Fatalf("NewPromptManager() error = %v", err)
	}

	rendered, err := pm.Render("rerank_precision", nil)
	if err != nil {
		t.Fatalf("Render(rerank_precision, nil) error = %v", err)
	}

	if strings.Contains(rendered, "{{.Query}}") {
		t.Error("Render(nil) should NOT contain {{.Query}} — it should be rendered")
	}
	if !strings.Contains(rendered, "<no value>") {
		t.Error("Render(nil) should produce '<no value>' for missing keys, confirming the bug scenario")
	}
}

func TestPromptManager_Raw_Vs_Render_Nil(t *testing.T) {
	pm, err := NewPromptManager()
	if err != nil {
		t.Fatalf("NewPromptManager() error = %v", err)
	}

	raw, _ := pm.Raw("rerank_precision")
	rendered, _ := pm.Render("rerank_precision", nil)

	if raw == rendered {
		t.Error("Raw() and Render(nil) should differ — Render(nil) replaces template vars with <no value>")
	}
}
