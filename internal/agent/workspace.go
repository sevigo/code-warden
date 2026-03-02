package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// agentWorkspace holds the prepared workspace state for a session.
type agentWorkspace struct {
	dir     string
	logPath string
	logFile *os.File
}

// prepareAgentWorkspace creates the workspace directory, clones the project,
// writes the opencode config, registers it with the MCP server, and opens the
// log file. The caller is responsible for closing logFile and calling
// mcpServer.UnregisterWorkspace.
func (o *Orchestrator) prepareAgentWorkspace(ctx context.Context, session *Session) (*agentWorkspace, error) {
	workspaceDir := filepath.Join(o.config.WorkingDir, session.ID)
	if err := os.MkdirAll(workspaceDir, 0750); err != nil {
		return nil, fmt.Errorf("failed to create workspace directory %s: %w", workspaceDir, err)
	}

	o.logger.Info("runAgentCLI: preparing workspace", "session_id", session.ID, "dir", workspaceDir)
	if err := o.prepareWorkspace(ctx, workspaceDir); err != nil {
		return nil, fmt.Errorf("failed to prepare workspace at %s: %w", workspaceDir, err)
	}

	if err := o.writeOpencodeConfig(workspaceDir, session.ID); err != nil {
		return nil, fmt.Errorf("failed to write opencode config: %w", err)
	}

	o.mcpServer.RegisterWorkspace(session.ID, workspaceDir)

	logPath := filepath.Join(workspaceDir, "agent.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		o.mcpServer.UnregisterWorkspace(session.ID)
		return nil, fmt.Errorf("failed to create log file: %w", err)
	}

	return &agentWorkspace{
		dir:     workspaceDir,
		logPath: logPath,
		logFile: logFile,
	}, nil
}

// prepareWorkspace clones the project into the isolated workspace and sets the remote origin to the upstream URL.
func (o *Orchestrator) prepareWorkspace(ctx context.Context, destDir string) error {
	// First, clone from the local project root for speed
	//nolint:gosec // G204: Subprocess launched with variable arguments - intentional for workspace preparation
	cloneCmd := exec.CommandContext(ctx, "git", "clone", o.projectRoot, ".")
	cloneCmd.Dir = destDir
	if output, err := cloneCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clone failed: %w (output: %s)", err, string(output))
	}

	// Now detect the upstream origin URL from the primary project root
	remoteCmd := exec.CommandContext(ctx, "git", "remote", "get-url", "origin")
	remoteCmd.Dir = o.projectRoot
	remoteOutput, err := remoteCmd.Output()
	if err != nil {
		o.logger.Warn("failed to detect project origin URL, push_branch may fail", "error", err)
		return nil // Non-fatal, but push will fail later
	}
	upstreamURL := strings.TrimSpace(string(remoteOutput))

	// Set origin to the actual upstream URL in the workspace
	setRemoteCmd := exec.CommandContext(ctx, "git", "remote", "set-url", "origin", upstreamURL)
	setRemoteCmd.Dir = destDir
	if output, err := setRemoteCmd.CombinedOutput(); err != nil {
		o.logger.Warn("failed to set workspace origin to upstream URL", "url", upstreamURL, "error", err, "output", string(output))
	} else {
		o.logger.Info("workspace origin set to upstream URL", "url", upstreamURL)
	}

	return nil
}

// writeOpencodeConfig writes opencode.json into the workspace directory so
// OpenCode discovers the per-session MCP server via working directory config.
func (o *Orchestrator) writeOpencodeConfig(workspaceDir, sessionID string) error {
	config := fmt.Sprintf(`{
  "mcp": {
    "code-warden": {
      "type": "remote",
      "url": "http://%s/sse?workspace=%s",
      "enabled": true
    }
  }
}`, o.config.MCPAddr, sessionID)

	path := filepath.Join(workspaceDir, "opencode.json")
	return os.WriteFile(path, []byte(config), 0600)
}

// cleanupWorkspace removes the session's isolated workspace directory.
// This should be called after the session completes (success or failure).
func (o *Orchestrator) cleanupWorkspace(sessionID string) {
	workspaceDir := filepath.Join(o.config.WorkingDir, sessionID)
	if workspaceDir == "" || workspaceDir == "/" {
		// Safety check: don't delete root or empty paths
		o.logger.Warn("cleanupWorkspace: invalid workspace path, skipping cleanup", "session_id", sessionID)
		return
	}

	// Check if directory exists before attempting removal
	if _, err := os.Stat(workspaceDir); os.IsNotExist(err) {
		o.logger.Debug("cleanupWorkspace: workspace already removed", "session_id", sessionID)
		return
	}

	if err := os.RemoveAll(workspaceDir); err != nil {
		o.logger.Warn("cleanupWorkspace: failed to remove workspace",
			"session_id", sessionID,
			"path", workspaceDir,
			"error", err)
		return
	}

	o.logger.Info("cleanupWorkspace: workspace removed",
		"session_id", sessionID,
		"path", workspaceDir)
}

// readLogFile reads the content of a file up to maxBytes.
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
