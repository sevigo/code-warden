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
