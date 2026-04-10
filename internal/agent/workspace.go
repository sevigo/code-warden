package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// agentWorkspace holds the prepared workspace state for a session.
type agentWorkspace struct {
	dir       string
	logPath   string
	tracePath string // absolute path to trace.jsonl (empty if tracing disabled)
	logFile   *os.File
	traceFile *os.File // JSONL conversation trace for post-mortem debugging (nil = not opened)
}

// prepareAgentWorkspace creates the workspace directory, clones the project,
// registers it with the MCP server, and opens the log file.
// The caller is responsible for closing logFile and calling
// mcpServer.UnregisterWorkspace.
func (o *Orchestrator) prepareAgentWorkspace(ctx context.Context, session *Session) (*agentWorkspace, error) {
	// Sanitize session ID to prevent path traversal
	safeID, err := sanitizeSessionID(session.ID)
	if err != nil {
		return nil, fmt.Errorf("invalid session ID: %w", err)
	}

	workspaceDir := filepath.Join(o.config.WorkingDir, safeID)
	if err := os.MkdirAll(workspaceDir, 0750); err != nil {
		return nil, fmt.Errorf("failed to create workspace directory %s: %w", workspaceDir, err)
	}

	o.logger.Info("runAgentCLI: preparing workspace", "session_id", session.ID, "dir", workspaceDir)
	if err := o.prepareWorkspace(ctx, workspaceDir); err != nil {
		return nil, fmt.Errorf("failed to prepare workspace at %s: %w", workspaceDir, err)
	}

	// Repoint origin to GitHub remote for pushing changes
	remoteURL := fmt.Sprintf("https://github.com/%s/%s.git", session.Issue.RepoOwner, session.Issue.RepoName)
	logURL := remoteURL // URL without token for safe logging

	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		token = os.Getenv("GH_TOKEN")
	}
	if token != "" {
		remoteURL = fmt.Sprintf("https://x-access-token:%s@github.com/%s/%s.git", token, session.Issue.RepoOwner, session.Issue.RepoName)
	}
	// TODO: Replace env var fallback with installation token from o.ghClient when available
	//nolint:gosec // G702: remoteURL constructed from GitHub API (RepoOwner/RepoName are trusted)
	setRemoteCmd := exec.CommandContext(ctx, "git", "remote", "set-url", "origin", remoteURL)
	setRemoteCmd.Dir = workspaceDir
	if output, err := setRemoteCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("failed to set workspace origin to GitHub upstream: %w (output: %s)", err, string(output))
	}
	o.logger.Info("workspace origin set to GitHub upstream", "url", logURL)

	o.mcpServer.RegisterWorkspace(session.ID, workspaceDir)

	logPath := filepath.Join(workspaceDir, "agent.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		o.mcpServer.UnregisterWorkspace(session.ID)
		return nil, fmt.Errorf("failed to create log file: %w", err)
	}

	// Open JSONL trace file for per-iteration conversation snapshots.
	// Written by buildCompactionHook after every loop iteration.
	// Non-fatal if creation fails — tracing is best-effort.
	tracePath := filepath.Join(workspaceDir, "trace.jsonl")
	traceFile, err := os.Create(tracePath)
	if err != nil {
		o.logger.Warn("failed to create trace file, tracing disabled", "error", err)
		traceFile = nil
	}

	// Start LSP manager removed — agent uses run_command("go build ./...")
	// for compile checks instead. 30-120s LSP startup is not worth it
	// when the model can verify with `make lint` / `make test`.

	return &agentWorkspace{
		dir:       workspaceDir,
		logPath:   logPath,
		tracePath: tracePath,
		logFile:   logFile,
		traceFile: traceFile,
	}, nil
}

// prepareWorkspace clones the project into the isolated workspace.
func (o *Orchestrator) prepareWorkspace(ctx context.Context, destDir string) error {
	// Clone from the local project root for speed (avoids network round-trip).
	o.logger.Info("workspace: cloning project", "src", o.projectRoot, "dest", destDir)
	//nolint:gosec // G204: Subprocess launched with variable arguments - intentional for workspace preparation
	cloneCmd := exec.CommandContext(ctx, "git", "clone", o.projectRoot, ".")
	cloneCmd.Dir = destDir
	if output, err := cloneCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clone failed: %w (output: %s)", err, string(output))
	}
	o.logger.Info("workspace: clone complete", "dest", destDir)
	return nil
}

// cleanupWorkspace removes the session's isolated workspace directory.
// This should be called after the session completes (success or failure).
// Returns an error if cleanup fails.
func (o *Orchestrator) cleanupWorkspace(sessionID string) error {
	// Sanitize session ID for safety
	safeID, err := sanitizeSessionID(sessionID)
	if err != nil {
		return fmt.Errorf("invalid session ID: %w", err)
	}

	workspaceDir := filepath.Join(o.config.WorkingDir, safeID)
	if workspaceDir == "" || workspaceDir == "/" {
		// Safety check: don't delete root or empty paths
		return fmt.Errorf("invalid workspace path: %s", workspaceDir)
	}

	// Check if directory exists before attempting removal
	if _, err := os.Stat(workspaceDir); os.IsNotExist(err) {
		o.logger.Debug("cleanupWorkspace: workspace already removed", "session_id", safeID)
		return nil
	}

	if err := os.RemoveAll(workspaceDir); err != nil {
		o.logger.Warn("cleanupWorkspace: failed to remove workspace",
			"session_id", safeID,
			"path", workspaceDir,
			"error", err)
		return err
	}

	o.logger.Info("cleanupWorkspace: workspace removed",
		"session_id", safeID,
		"path", workspaceDir)
	return nil
}

// persistLogs copies agent.log and trace.jsonl from the workspace to a stable
// directory that survives cleanupWorkspace. This enables post-mortem analysis
// of sessions even after the workspace directory has been removed.
func (o *Orchestrator) persistLogs(ws *agentWorkspace, sessionID string) {
	safeID, err := sanitizeSessionID(sessionID)
	if err != nil {
		o.logger.Warn("persistLogs: invalid session ID", "error", err)
		return
	}

	// Stable directory: sibling of the workspace parent, e.g. /tmp/code-warden-agents/../agent-traces/
	traceDir := filepath.Join(filepath.Dir(o.config.WorkingDir), "agent-traces")
	if err := os.MkdirAll(traceDir, 0750); err != nil {
		o.logger.Warn("persistLogs: failed to create trace directory", "path", traceDir, "error", err)
		return
	}

	// Sync+close the files before copying to ensure all data is flushed.
	if ws.logFile != nil {
		_ = ws.logFile.Sync()
	}
	if ws.traceFile != nil {
		_ = ws.traceFile.Sync()
	}

	// Copy agent.log
	srcLog := filepath.Join(ws.dir, "agent.log")
	dstLog := filepath.Join(traceDir, safeID+".log")
	if err := copyFile(srcLog, dstLog); err != nil {
		o.logger.Debug("persistLogs: could not copy agent.log", "error", err)
	} else {
		o.logger.Info("persistLogs: saved agent.log", "path", dstLog)
	}

	// Copy trace.jsonl
	if ws.tracePath != "" {
		dstTrace := filepath.Join(traceDir, safeID+".jsonl")
		if err := copyFile(ws.tracePath, dstTrace); err != nil {
			o.logger.Debug("persistLogs: could not copy trace.jsonl", "error", err)
		} else {
			o.logger.Info("persistLogs: saved trace.jsonl", "path", dstTrace)
		}
	}
}

// copyFile copies a single file from src to dst. It returns an error if the
// source file does not exist — callers should treat this as non-fatal.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

// If the file exceeds maxBytes, it reads the *last* maxBytes to ensure
// the AGENT_RESULT: sentinel (typically at the end) is captured.
func (o *Orchestrator) readLogFile(path string, maxBytes int64) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	size := info.Size()
	var offset int64
	readSize := size

	if size > maxBytes {
		o.logger.Warn("readLogFile: log file exceeds size cap, truncating read from the end",
			"path", path, "size", size, "cap", maxBytes)
		offset = size - maxBytes
		readSize = maxBytes

		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return nil, fmt.Errorf("failed to seek in log file: %w", err)
		}
	}

	// Read up to readSize
	buf := make([]byte, readSize)
	_, err = io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}

	return buf, nil
}
