package lsp

import (
	"context"
	"io/fs"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Manager starts and manages LSP servers for all languages present in a workspace.
// It is the primary entry point for LSP functionality in an agent session.
//
// Usage:
//
//	mgr := lsp.NewManager(workspaceDir, lsp.DefaultServers()...)
//	if err := mgr.Start(ctx); err != nil { /* LSP unavailable, continue without */ }
//	defer mgr.Stop()
type Manager struct {
	workspace string
	registry  []LanguageServer

	mu      sync.RWMutex
	clients map[string]*Client // language name -> active client
	logger  *slog.Logger
}

// NewManager creates a Manager for the given workspace directory.
// servers is the list of language servers to consider starting;
// use DefaultServers() for the built-in set or provide a custom list.
func NewManager(workspace string, logger *slog.Logger, servers ...LanguageServer) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		workspace: workspace,
		registry:  servers,
		clients:   make(map[string]*Client),
		logger:    logger,
	}
}

// Start detects the languages present in the workspace and starts the
// appropriate language servers. Servers whose binaries are not found in
// PATH are silently skipped — LSP is always optional.
func (m *Manager) Start(ctx context.Context) error {
	detected := m.detectLanguages()
	if len(detected) == 0 {
		m.logger.Info("lsp: no supported languages detected in workspace")
		return nil
	}

	for _, srv := range detected {
		if err := m.startServer(ctx, srv); err != nil {
			m.logger.Warn("lsp: failed to start server, skipping",
				"lang", srv.Name(), "error", err)
		}
	}
	return nil
}

// Stop gracefully shuts down all running language servers.
func (m *Manager) Stop() {
	m.mu.Lock()
	clients := make(map[string]*Client, len(m.clients))
	for k, v := range m.clients {
		clients[k] = v
	}
	m.clients = make(map[string]*Client)
	m.mu.Unlock()

	var wg sync.WaitGroup
	for name, c := range clients {
		wg.Add(1)
		go func(name string, c *Client) {
			defer wg.Done()
			c.Stop()
			m.logger.Info("lsp: server stopped", "lang", name)
		}(name, c)
	}
	wg.Wait()
}

// Available reports whether any language server is running.
func (m *Manager) Available() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.clients) > 0
}

// NotifyChange tells the relevant language server that a file has changed
// and returns any diagnostics for that file. This is the primary hook
// called after write_file and edit_file tool executions.
//
// It is safe to call with a nil receiver — it returns nil, nil gracefully.
func (m *Manager) NotifyChange(ctx context.Context, absPath, content string) ([]Diagnostic, error) {
	if m == nil {
		return nil, nil
	}
	c := m.clientForFile(absPath)
	if c == nil {
		return nil, nil
	}

	langID := m.languageIDForFile(absPath)

	// Notify the server. Try DidChange first; if the file was never opened, use DidOpen.
	if err := c.DidChange(ctx, absPath, content); err != nil {
		_ = c.DidOpen(ctx, absPath, langID, content)
	}

	// Give the server a moment to process.
	select {
	case <-time.After(700 * time.Millisecond):
	case <-ctx.Done():
		return nil, nil
	}

	diags, err := c.Diagnostics(ctx, absPath)
	if err != nil {
		m.logger.Debug("lsp: diagnostics request failed", "file", absPath, "error", err)
		return nil, nil // non-fatal
	}
	return diags, nil
}

// Diagnostics returns the current diagnostics for the given file.
func (m *Manager) Diagnostics(ctx context.Context, absPath string) ([]Diagnostic, error) {
	if m == nil {
		return nil, nil
	}
	c := m.clientForFile(absPath)
	if c == nil {
		return nil, nil
	}
	return c.Diagnostics(ctx, absPath)
}

// Definition returns the definition location(s) for the symbol at the given position.
func (m *Manager) Definition(ctx context.Context, absPath string, line, col int) ([]Location, error) {
	if m == nil {
		return nil, nil
	}
	c := m.clientForFile(absPath)
	if c == nil {
		return nil, nil
	}
	return c.Definition(ctx, absPath, line, col)
}

// References returns all references to the symbol at the given position.
func (m *Manager) References(ctx context.Context, absPath string, line, col int) ([]Location, error) {
	if m == nil {
		return nil, nil
	}
	c := m.clientForFile(absPath)
	if c == nil {
		return nil, nil
	}
	return c.References(ctx, absPath, line, col)
}

// Hover returns hover documentation for the symbol at the given position.
func (m *Manager) Hover(ctx context.Context, absPath string, line, col int) (string, error) {
	if m == nil {
		return "", nil
	}
	c := m.clientForFile(absPath)
	if c == nil {
		return "", nil
	}
	return c.Hover(ctx, absPath, line, col)
}

// --- internal ---

func (m *Manager) startServer(ctx context.Context, srv LanguageServer) error {
	cmd := srv.Command(m.workspace)
	if len(cmd) == 0 {
		return nil
	}

	// Check that the binary exists before trying to start.
	if _, err := exec.LookPath(cmd[0]); err != nil {
		return err
	}

	client, err := newClient(ctx, m.workspace, cmd, srv.Env())
	if err != nil {
		return err
	}

	m.mu.Lock()
	m.clients[srv.Name()] = client
	m.mu.Unlock()

	m.logger.Info("lsp: server started", "lang", srv.Name(), "cmd", strings.Join(cmd, " "))
	return nil
}

// detectLanguages scans the workspace for file extensions and returns the
// registered LanguageServer implementations that match.
func (m *Manager) detectLanguages() []LanguageServer {
	extSet := make(map[string]struct{})
	_ = filepath.WalkDir(m.workspace, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			if d != nil && d.IsDir() && shouldSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		extSet[strings.ToLower(filepath.Ext(path))] = struct{}{}
		return nil
	})

	var matched []LanguageServer
	seen := make(map[string]bool)
	for _, srv := range m.registry {
		for _, ext := range srv.Extensions() {
			if _, ok := extSet[ext]; ok {
				if !seen[srv.Name()] {
					matched = append(matched, srv)
					seen[srv.Name()] = true
				}
				break
			}
		}
	}
	return matched
}

// clientForFile returns the Client responsible for the given file,
// based on file extension. Returns nil if no server handles this extension.
func (m *Manager) clientForFile(absPath string) *Client {
	ext := strings.ToLower(filepath.Ext(absPath))
	for _, srv := range m.registry {
		for _, srvExt := range srv.Extensions() {
			if srvExt == ext {
				m.mu.RLock()
				c := m.clients[srv.Name()]
				m.mu.RUnlock()
				return c
			}
		}
	}
	return nil
}

// languageIDForFile returns the LSP languageId for the given file path.
func (m *Manager) languageIDForFile(absPath string) string {
	ext := strings.ToLower(filepath.Ext(absPath))
	for _, srv := range m.registry {
		for _, srvExt := range srv.Extensions() {
			if srvExt == ext {
				return srv.LanguageID()
			}
		}
	}
	return "plaintext"
}

// shouldSkipDir returns true for directories that don't contain source files
// and would just slow down the file extension scan.
func shouldSkipDir(name string) bool {
	switch name {
	case ".git", "vendor", "node_modules", ".cache", "dist", "build", "bin", "obj":
		return true
	}
	return false
}
