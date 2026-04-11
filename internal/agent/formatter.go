package agent

import (
	"context"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// formatterSpec describes a single formatter pass: the binary name and a
// function that builds its argument list for a given file path.
type formatterSpec struct {
	binary string
	args   func(filePath string) []string
}

// defaultFormatters maps file extensions to ordered lists of formatter specs.
// The first spec whose binary is found on PATH wins (e.g. goimports before gofmt).
var defaultFormatters = map[string][]formatterSpec{
	".go": {
		{binary: "goimports", args: func(f string) []string { return []string{"-w", f} }},
		{binary: "gofmt", args: func(f string) []string { return []string{"-w", f} }},
	},
	".py": {
		{binary: "ruff", args: func(f string) []string { return []string{"format", f} }},
		{binary: "ruff", args: func(f string) []string { return []string{"check", "--fix", "--select", "I,F401", f} }},
	},
	".ts":   {{binary: "prettier", args: func(f string) []string { return []string{"--write", f} }}},
	".tsx":  {{binary: "prettier", args: func(f string) []string { return []string{"--write", f} }}},
	".js":   {{binary: "prettier", args: func(f string) []string { return []string{"--write", f} }}},
	".jsx":  {{binary: "prettier", args: func(f string) []string { return []string{"--write", f} }}},
	".rs":   {{binary: "rustfmt", args: func(f string) []string { return []string{f} }}},
	".java": {{binary: "google-java-format", args: func(f string) []string { return []string{"--replace", f} }}},
}

// Formatter runs language-specific formatters on files after they are written
// by the agent. If a formatter binary is not installed it is silently skipped.
// Errors are logged but never propagated to callers — formatting is best-effort.
type Formatter struct {
	logger *slog.Logger
}

// NewFormatter creates a Formatter with the given logger.
func NewFormatter(logger *slog.Logger) *Formatter {
	return &Formatter{logger: logger}
}

// Format runs the appropriate formatter for filePath's extension.
// The cmd is executed with workspaceRoot as its working directory so that
// project-local config files (.prettierrc, pyproject.toml, etc.) are respected.
// Returns nil if no formatter is available or the formatter succeeds.
// Returns an error only if something unexpected happens (context cancelled, etc.).
func (f *Formatter) Format(ctx context.Context, workspaceRoot, filePath string) error {
	if f == nil || f.logger == nil {
		return nil
	}
	ext := strings.ToLower(filepath.Ext(filePath))
	specs, ok := defaultFormatters[ext]
	if !ok {
		return nil
	}

	for _, spec := range specs {
		if _, err := exec.LookPath(spec.binary); err != nil {
			continue
		}

		args := spec.args(filePath)
		formatCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		cmd := exec.CommandContext(formatCtx, spec.binary, args...) //nolint:gosec // G204: binary is from our hardcoded map, not user input
		cmd.Dir = workspaceRoot
		out, err := cmd.CombinedOutput()
		if err != nil {
			f.logger.Warn("auto-format: formatter failed",
				"binary", spec.binary, "path", filePath, "error", err, "output", string(out))
			return err
		}
		f.logger.Debug("auto-format: formatted", "binary", spec.binary, "path", filePath)
		return nil
	}

	f.logger.Debug("auto-format: no formatter found", "ext", ext, "path", filePath)
	return nil
}
