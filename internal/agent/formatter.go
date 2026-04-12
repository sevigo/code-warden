package agent

import (
	"context"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sevigo/code-warden/internal/core"
)

// Formatter runs language-specific formatters on files written by the agent.
// Only Go files are auto-formatted per-write (goimports resolves imports without
// an LSP — a unique capability). All other formatting is handled by a batch
// format_command run once before the review phase (configured in .code-warden.yml).
type Formatter struct {
	logger *slog.Logger
}

// NewFormatter creates a Formatter with the given logger.
func NewFormatter(logger *slog.Logger) *Formatter {
	return &Formatter{logger: logger}
}

// newFormatterFromConfig creates a Formatter unless per-write formatting is
// disabled by the repo config. The batch format_command is independent and
// controlled by its own field.
func newFormatterFromConfig(logger *slog.Logger, cfg *core.RepoConfig) *Formatter {
	if cfg != nil && cfg.DisableFormatOnWrite {
		return nil
	}
	return NewFormatter(logger)
}

// Format runs the appropriate formatter for filePath's extension.
// For Go files: try goimports first (resolves imports), fall back to gofmt.
// All other extensions are skipped — formatting for those is handled by the
// batch format_command before the review phase.
// Nil receiver is a no-op.
func (f *Formatter) Format(ctx context.Context, workspaceRoot, filePath string) {
	if f == nil {
		return
	}
	ext := strings.ToLower(filepath.Ext(filePath))
	if ext != ".go" {
		return
	}

	for _, binary := range []string{"goimports", "gofmt"} {
		if _, err := exec.LookPath(binary); err != nil {
			continue
		}
		f.runSpec(ctx, binary, []string{"-w", filePath}, workspaceRoot)
		return
	}
	f.logger.Debug("auto-format: no Go formatter available", "path", filePath)
}

// FormatProject runs a single batch format command on the workspace root.
// This is called once between the edit and review phases, using the project's
// own format_command from .code-warden.yml (e.g. "npm run format", "ruff format .").
// Nil receiver or empty command is a no-op. Returns true only when the command
// succeeds, so callers can decide whether to notify the LLM about formatting changes.
func (f *Formatter) FormatProject(ctx context.Context, workspaceRoot, command string) bool {
	if f == nil || command == "" {
		return false
	}
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return false
	}

	fmtCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(fmtCtx, parts[0], parts[1:]...) //nolint:gosec // G204: command from repo config
	cmd.Dir = workspaceRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.logger.Warn("auto-format: project format command failed",
			"command", command, "error", err, "output", string(out))
		return false
	}
	f.logger.Info("auto-format: project formatted", "command", command)
	return true
}

// runSpec executes a single formatter pass with a scoped 30-second timeout.
func (f *Formatter) runSpec(ctx context.Context, binary string, args []string, workspaceRoot string) {
	fmtCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(fmtCtx, binary, args...)
	cmd.Dir = workspaceRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.logger.Warn("auto-format: formatter failed",
			"binary", binary, "args", args, "error", err, "output", string(out))
		return
	}
	f.logger.Debug("auto-format: formatted", "binary", binary, "args", args)
}
