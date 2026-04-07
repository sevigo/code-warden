package tools

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/sevigo/code-warden/internal/core"
)

const (
	// defaultCommandTimeout is the maximum time a command may run.
	defaultCommandTimeout = 5 * time.Minute

	// maxOutputBytes is the maximum number of bytes captured from stdout+stderr.
	maxOutputBytes = 256 * 1024 // 256 KB
)

// defaultVerifyCommands are the commands used when no verify_commands are
// configured in .code-warden.yml.
var defaultVerifyCommands = []string{"make lint", "make test"}

// RunCommand executes a whitelisted shell command in the session workspace and
// returns stdout, stderr, and the exit code.  Only commands listed in
// RepoConfig.VerifyCommands (or the built-in defaults) are allowed.
type RunCommand struct {
	RepoConfig  *core.RepoConfig
	ProjectRoot string
	Logger      *slog.Logger
}

// RunCommandResponse is the response for the run_command tool.
type RunCommandResponse struct {
	Command  string `json:"command"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	Success  bool   `json:"success"`
}

func (t *RunCommand) Name() string {
	return "run_command"
}

func (t *RunCommand) Description() string {
	return `Run a whitelisted verification command in the project workspace.

Only commands defined in the repository's verify_commands configuration are
allowed (defaults: "make lint", "make test").

Use this tool to verify that the code compiles and all tests pass before
calling review_code or create_pull_request.

Returns stdout, stderr, exit_code, and a boolean success field.`
}

func (t *RunCommand) ParametersSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "The command to run (must be in the allowed list)",
			},
		},
		"required": []string{"command"},
	}
}

func (t *RunCommand) Execute(ctx context.Context, args map[string]any) (any, error) {
	command, ok := args["command"].(string)
	if !ok || command == "" {
		return nil, fmt.Errorf("command is required")
	}
	command = strings.TrimSpace(command)

	allowed := t.allowedCommands()
	if !isAllowed(command, allowed) {
		return nil, fmt.Errorf("command %q is not in the allowed list: %v", command, allowed)
	}

	projectRoot := ProjectRootFromContext(ctx)
	if projectRoot == "" {
		projectRoot = t.ProjectRoot
	}
	if projectRoot == "" {
		return nil, fmt.Errorf("project root is not set")
	}

	t.Logger.Info("run_command: executing", "command", command, "dir", projectRoot)

	// Apply a hard timeout independent of the parent context.
	runCtx, cancel := context.WithTimeout(ctx, defaultCommandTimeout)
	defer cancel()

	parts := strings.Fields(command)
	cmd := exec.CommandContext(runCtx, parts[0], parts[1:]...) //nolint:gosec // command is validated against whitelist
	cmd.Dir = projectRoot

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	exitCode := 0
	if err != nil {
		if exitErr, ok2 := err.(*exec.ExitError); ok2 {
			exitCode = exitErr.ExitCode()
		} else {
			// Context timeout or other exec error.
			return nil, fmt.Errorf("failed to run command %q: %w", command, err)
		}
	}

	stdoutStr := truncateOutput(stdout.String(), maxOutputBytes/2)
	stderrStr := truncateOutput(stderr.String(), maxOutputBytes/2)

	t.Logger.Info("run_command: completed",
		"command", command,
		"exit_code", exitCode,
		"stdout_len", len(stdoutStr),
		"stderr_len", len(stderrStr))

	return RunCommandResponse{
		Command:  command,
		Stdout:   stdoutStr,
		Stderr:   stderrStr,
		ExitCode: exitCode,
		Success:  exitCode == 0,
	}, nil
}

// allowedCommands returns the list of permitted commands, falling back to
// the built-in defaults when the repo config is empty.
func (t *RunCommand) allowedCommands() []string {
	if t.RepoConfig != nil && len(t.RepoConfig.VerifyCommands) > 0 {
		return t.RepoConfig.VerifyCommands
	}
	return defaultVerifyCommands
}

// isAllowed returns true if command exactly matches one of the allowed entries.
func isAllowed(command string, allowed []string) bool {
	for _, a := range allowed {
		if strings.TrimSpace(a) == command {
			return true
		}
	}
	return false
}

// truncateOutput trims output to at most maxBytes, appending a notice when
// truncation occurs.
func truncateOutput(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	const notice = "\n[... output truncated ...]"
	return s[:maxBytes-len(notice)] + notice
}
