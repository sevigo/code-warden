package mcp

import (
	"context"
	"log/slog"
	"os"
	"testing"

	goframeagent "github.com/sevigo/goframe/agent"
)

func TestGovernanceConfig_Default(t *testing.T) {
	cfg := DefaultGovernanceConfig()
	if cfg.EnableGovernance {
		t.Error("Default governance should be disabled")
	}
	if len(cfg.AllowedTools) != 0 {
		t.Error("Default allowed tools should be empty")
	}
	if len(cfg.DeniedTools) != 0 {
		t.Error("Default denied tools should be empty")
	}
}

func TestSetupGovernance_Disabled(t *testing.T) {
	s := &Server{
		registry: goframeagent.NewRegistry(),
		logger:   slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}

	cfg := GovernanceConfig{EnableGovernance: false}
	s.SetupGovernance(cfg)

	if s.governance != nil {
		t.Error("Governance should be nil when disabled")
	}
}

func TestSetupGovernance_PermissionCheck(t *testing.T) {
	s := &Server{
		registry: goframeagent.NewRegistry(),
		logger:   slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}

	cfg := GovernanceConfig{
		EnableGovernance: true,
		AllowedTools:     []string{"search_code", "get_symbol"},
		DeniedTools:      []string{"push_branch"},
	}
	s.SetupGovernance(cfg)

	if s.governance == nil {
		t.Fatal("Governance should not be nil when enabled")
	}

	// Test that allowed tool passes
	err := s.governance.Validate(context.Background(), "search_code", nil)
	if err != nil {
		t.Errorf("Allowed tool should pass: %v", err)
	}

	// Test that denied tool fails
	err = s.governance.Validate(context.Background(), "push_branch", nil)
	if err == nil {
		t.Error("Denied tool should fail validation")
	}

	// Test that non-listed tool fails (when AllowedTools is set)
	err = s.governance.Validate(context.Background(), "unknown_tool", nil)
	if err == nil {
		t.Error("Non-allowed tool should fail when AllowedTools is set")
	}
}

func TestSetupGovernance_RateLimits(t *testing.T) {
	s := &Server{
		registry: goframeagent.NewRegistry(),
		logger:   slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}

	cfg := GovernanceConfig{
		EnableGovernance: true,
		RateLimits: map[string]int{
			"review_code": 2,
		},
	}
	s.SetupGovernance(cfg)

	if s.governance == nil {
		t.Fatal("Governance should not be nil when enabled")
	}

	// First call should pass
	err := s.governance.Validate(context.Background(), "review_code", nil)
	if err != nil {
		t.Errorf("First call should pass: %v", err)
	}

	// Second call should pass
	err = s.governance.Validate(context.Background(), "review_code", nil)
	if err != nil {
		t.Errorf("Second call should pass: %v", err)
	}

	// Third call should fail (rate limit exceeded)
	err = s.governance.Validate(context.Background(), "review_code", nil)
	if err == nil {
		t.Error("Third call should fail rate limit")
	}
}

func TestSetupGovernance_EmptyRules(t *testing.T) {
	// Test that governance with no rules logs a warning but still works
	s := &Server{
		registry: goframeagent.NewRegistry(),
		logger:   slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}

	cfg := GovernanceConfig{
		EnableGovernance: true,
	}
	s.SetupGovernance(cfg)

	// Governance should still be enabled
	if s.governance == nil {
		t.Error("Governance should not be nil when enabled with empty rules")
	}

	// All tools should pass since there are no rules
	err := s.governance.Validate(context.Background(), "any_tool", nil)
	if err != nil {
		t.Errorf("Empty governance should allow all tools: %v", err)
	}
}

func TestCallTool_WithGovernance(t *testing.T) {
	// Create a minimal server with governance
	s := &Server{
		registry:   goframeagent.NewRegistry(),
		governance: goframeagent.NewGovernance(goframeagent.NewPermissionCheck().Deny("blocked_tool")),
		logger:     slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}

	// Test that blocked tool returns error
	_, err := s.CallTool(context.Background(), "blocked_tool", nil)
	if err == nil {
		t.Error("Blocked tool should return error")
	}
}

func TestListTools(t *testing.T) {
	// Create a registry and register a tool
	registry := goframeagent.NewRegistry()

	tool := &testTool{
		name:        "test_tool",
		description: "A test tool for verification",
		schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "A test query",
				},
			},
			"required": []string{"query"},
		},
	}

	err := registry.Register(tool)
	if err != nil {
		t.Fatalf("Failed to register tool: %v", err)
	}

	s := &Server{
		registry: registry,
		logger:   slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}

	// Get tool list
	tools := s.ListTools()

	if len(tools) != 1 {
		t.Fatalf("Expected 1 tool, got %d", len(tools))
	}

	// Verify tool info
	if tools[0].Name != "test_tool" {
		t.Errorf("Expected name 'test_tool', got %q", tools[0].Name)
	}
	if tools[0].Description != "A test tool for verification" {
		t.Errorf("Expected description 'A test tool for verification', got %q", tools[0].Description)
	}
	if tools[0].InputSchema == nil {
		t.Error("InputSchema should not be nil")
	}
}

func TestListTools_SortedOrder(t *testing.T) {
	registry := goframeagent.NewRegistry()

	// Register tools in random order
	tools := []struct {
		name string
		desc string
	}{
		{"zebra_tool", "Zebra description"},
		{"alpha_tool", "Alpha description"},
		{"beta_tool", "Beta description"},
	}

	for _, tc := range tools {
		err := registry.Register(&testTool{
			name:        tc.name,
			description: tc.desc,
			schema:      map[string]any{"type": "object"},
		})
		if err != nil {
			t.Fatalf("Failed to register tool %s: %v", tc.name, err)
		}
	}

	s := &Server{
		registry: registry,
		logger:   slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}

	// Get tool list
	list := s.ListTools()

	if len(list) != 3 {
		t.Fatalf("Expected 3 tools, got %d", len(list))
	}

	// Verify sorted order
	expected := []string{"alpha_tool", "beta_tool", "zebra_tool"}
	for i, name := range expected {
		if list[i].Name != name {
			t.Errorf("Expected tools[%d].Name = %q, got %q", i, name, list[i].Name)
		}
	}
}

// testTool is a simple test implementation of the Tool interface
type testTool struct {
	name        string
	description string
	schema      map[string]any
}

func (t *testTool) Name() string                     { return t.name }
func (t *testTool) Description() string              { return t.description }
func (t *testTool) ParametersSchema() map[string]any { return t.schema }
func (t *testTool) Execute(_ context.Context, _ map[string]any) (any, error) {
	return "test result", nil
}
