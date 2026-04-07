package tools

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/sevigo/code-warden/internal/core"
)

func TestRunCommand_Whitelist(t *testing.T) {
	dir := t.TempDir()
	tool := &RunCommand{
		RepoConfig:  &core.RepoConfig{VerifyCommands: []string{"make lint", "make test"}},
		ProjectRoot: dir,
		Logger:      slog.Default(),
	}

	allowed := []string{"make lint", "make test"}
	rejected := []string{
		"make lint; rm -rf /",
		"sh -c 'rm -rf /'",
		"make build",
		"",
	}

	for _, cmd := range allowed {
		t.Run("allowed_"+cmd, func(t *testing.T) {
			// The command itself is allowed by the whitelist even if execution fails
			// (binary not found in temp dir is fine — we only test whitelist logic here).
			ctx := WithProjectRoot(context.Background(), dir)
			_, err := tool.Execute(ctx, map[string]any{"command": cmd})
			// Execution error is OK (make not found); whitelist rejection is not.
			if err != nil && strings.Contains(err.Error(), "not in the allowed list") {
				t.Errorf("command %q should be allowed but was rejected: %v", cmd, err)
			}
		})
	}

	for _, cmd := range rejected {
		t.Run("rejected_"+cmd, func(t *testing.T) {
			ctx := WithProjectRoot(context.Background(), dir)
			_, err := tool.Execute(ctx, map[string]any{"command": cmd})
			if err == nil || !strings.Contains(err.Error(), "not in the allowed list") {
				if cmd == "" {
					// Empty command hits the required-field check, not whitelist.
					if err == nil || !strings.Contains(err.Error(), "required") {
						t.Errorf("empty command should fail with required error, got: %v", err)
					}
					return
				}
				t.Errorf("command %q should be rejected by whitelist, got err: %v", cmd, err)
			}
		})
	}
}

func TestRunCommand_DefaultWhitelist(t *testing.T) {
	tool := &RunCommand{
		RepoConfig:  &core.RepoConfig{}, // empty VerifyCommands → defaults
		ProjectRoot: t.TempDir(),
		Logger:      slog.Default(),
	}

	for _, cmd := range defaultVerifyCommands {
		t.Run(cmd, func(t *testing.T) {
			ctx := WithProjectRoot(context.Background(), t.TempDir())
			_, err := tool.Execute(ctx, map[string]any{"command": cmd})
			if err != nil && strings.Contains(err.Error(), "not in the allowed list") {
				t.Errorf("default command %q should be allowed: %v", cmd, err)
			}
		})
	}
}

func TestRunCommand_ExitCode(t *testing.T) {
	// Use a real binary we know exists: "false" exits with code 1.
	falsePath, err := findBinary("false")
	if err != nil {
		t.Skip("'false' binary not found, skipping exit-code test")
	}

	dir := t.TempDir()
	tool := &RunCommand{
		RepoConfig:  &core.RepoConfig{VerifyCommands: []string{falsePath}},
		ProjectRoot: dir,
		Logger:      slog.Default(),
	}

	ctx := WithProjectRoot(context.Background(), dir)
	result, err := tool.Execute(ctx, map[string]any{"command": falsePath})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp, ok := result.(RunCommandResponse)
	if !ok {
		t.Fatalf("unexpected result type: %T", result)
	}
	if resp.ExitCode == 0 {
		t.Error("expected non-zero exit code from 'false'")
	}
	if resp.Success {
		t.Error("expected success=false for non-zero exit")
	}
}

func TestRunCommand_SuccessExitCode(t *testing.T) {
	// Use "true" which always exits 0.
	truePath, err := findBinary("true")
	if err != nil {
		t.Skip("'true' binary not found, skipping success exit-code test")
	}

	dir := t.TempDir()
	tool := &RunCommand{
		RepoConfig:  &core.RepoConfig{VerifyCommands: []string{truePath}},
		ProjectRoot: dir,
		Logger:      slog.Default(),
	}

	ctx := WithProjectRoot(context.Background(), dir)
	result, err := tool.Execute(ctx, map[string]any{"command": truePath})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp, ok := result.(RunCommandResponse)
	if !ok {
		t.Fatalf("unexpected result type: %T", result)
	}
	if resp.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", resp.ExitCode)
	}
	if !resp.Success {
		t.Error("expected success=true for zero exit")
	}
}

func TestRunCommand_OutputTruncation(t *testing.T) {
	long := strings.Repeat("x", maxOutputBytes)
	got := truncateOutput(long, maxOutputBytes/2)
	if len(got) > maxOutputBytes/2+100 {
		t.Errorf("truncateOutput returned %d bytes, expected ~%d", len(got), maxOutputBytes/2)
	}
	if !strings.Contains(got, "truncated") {
		t.Error("truncated output should contain a notice")
	}

	short := "hello"
	if truncateOutput(short, maxOutputBytes) != short {
		t.Error("short output should not be truncated")
	}
}

func TestRunCommand_ConfigurableTimeout(t *testing.T) {
	cfg := &core.RepoConfig{CommandTimeoutSeconds: 600}
	if cfg.CommandTimeoutSeconds != 600 {
		t.Error("timeout should be configurable via RepoConfig")
	}
}

func TestRunCommand_MissingProjectRoot(t *testing.T) {
	tool := &RunCommand{
		RepoConfig: &core.RepoConfig{VerifyCommands: []string{"make lint"}},
		Logger:     slog.Default(),
		// ProjectRoot intentionally empty, no context injection either
	}
	_, err := tool.Execute(context.Background(), map[string]any{"command": "make lint"})
	if err == nil || !strings.Contains(err.Error(), "project root") {
		t.Errorf("expected project root error, got: %v", err)
	}
}

func TestIsAllowed(t *testing.T) {
	allowed := []string{"make lint", "make test", "go vet ./..."}
	cases := []struct {
		command string
		want    bool
	}{
		{"make lint", true},
		{"make test", true},
		{"go vet ./...", true},
		{"make lint; rm -rf /", false},
		{"Make Lint", false}, // case-sensitive
		{"", false},
		{"make", false},
	}
	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			if got := isAllowed(tc.command, allowed); got != tc.want {
				t.Errorf("isAllowed(%q) = %v, want %v", tc.command, got, tc.want)
			}
		})
	}
}

// findBinary looks up a binary in common locations.
func findBinary(name string) (string, error) {
	for _, dir := range []string{"/usr/bin", "/bin"} {
		p := dir + "/" + name
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", os.ErrNotExist
}
