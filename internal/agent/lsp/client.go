package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// diagMapMaxFiles is the maximum number of files whose diagnostics are kept
// in memory. When the cache exceeds this size the oldest entry is evicted.
const diagMapMaxFiles = 200

// Client is a JSON-RPC 2.0 client that speaks the Language Server Protocol
// over a stdio subprocess. It is safe for concurrent use.
type Client struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr io.ReadCloser

	mu      sync.Mutex // guards stdin writes and pending map
	pending map[int64]chan *response

	// Push-notification diagnostics cache: uri -> []Diagnostic
	diagMu  sync.RWMutex
	diagMap map[string][]Diagnostic

	nextID atomic.Int64
	done   chan struct{}
	wg     sync.WaitGroup
}

// newClient starts the given command as an LSP server and performs the
// LSP initialize / initialized handshake.
func newClient(ctx context.Context, workspaceDir string, command []string, env []string) (*Client, error) {
	if len(command) == 0 {
		return nil, fmt.Errorf("lsp: empty command")
	}

	//nolint:gosec // G204: command is from trusted LanguageServer.Command() implementations
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Dir = workspaceDir
	cmd.Env = append(os.Environ(), env...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("lsp: stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("lsp: stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("lsp: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		_ = stderrPipe.Close()
		return nil, fmt.Errorf("lsp: start %s: %w", command[0], err)
	}

	c := &Client{
		cmd:     cmd,
		stdin:   stdin,
		stdout:  bufio.NewReaderSize(stdoutPipe, 1<<20),
		stderr:  stderrPipe,
		pending: make(map[int64]chan *response),
		diagMap: make(map[string][]Diagnostic),
		done:    make(chan struct{}),
	}

	c.wg.Add(1)
	go c.readLoop()

	if err := c.initialize(ctx, workspaceDir); err != nil {
		c.Stop()
		return nil, fmt.Errorf("lsp: initialize: %w", err)
	}
	return c, nil
}

// Stop shuts down the LSP server gracefully.
func (c *Client) Stop() {
	shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_ = c.call(shutCtx, "shutdown", nil, nil)
	_ = c.notify("exit", nil)

	close(c.done)
	c.wg.Wait()
	_ = c.stdin.Close()
	_ = c.cmd.Wait()
}

// DidOpen notifies the server that a file has been opened.
func (c *Client) DidOpen(_ context.Context, absPath, languageID, content string) error {
	return c.notify("textDocument/didOpen", didOpenParams{
		TextDocument: textDocumentItem{
			URI:        pathToURI(absPath),
			LanguageID: languageID,
			Version:    1,
			Text:       content,
		},
	})
}

// DidChange notifies the server of a full document update.
func (c *Client) DidChange(_ context.Context, absPath, content string) error {
	return c.notify("textDocument/didChange", didChangeParams{
		TextDocument: versionedTextDocumentIdentifier{
			URI:     pathToURI(absPath),
			Version: int(time.Now().UnixMilli()), // monotonically increasing
		},
		ContentChanges: []textDocumentContentChangeEvent{{Text: content}},
	})
}

// Diagnostics pulls diagnostics for the given file (LSP 3.17 pull model).
// Falls back to the push-notification cache if pull is unsupported.
func (c *Client) Diagnostics(ctx context.Context, absPath string) ([]Diagnostic, error) {
	uri := pathToURI(absPath)

	// Try pull diagnostics first (LSP 3.17, supported by gopls ≥ 0.9).
	var report documentDiagnosticReport
	err := c.call(ctx, "textDocument/diagnostic", documentDiagnosticParams{
		TextDocument: textDocumentIdentifier{URI: uri},
	}, &report)
	if err == nil && report.Kind == "full" {
		return report.Items, nil
	}

	// Fall back to push-notification cache.
	c.diagMu.RLock()
	diags := c.diagMap[uri]
	c.diagMu.RUnlock()
	return diags, nil
}

// Definition returns the definition location for the symbol at the given position.
func (c *Client) Definition(ctx context.Context, absPath string, line, col int) ([]Location, error) {
	var locs []Location
	err := c.call(ctx, "textDocument/definition", textDocumentPositionParams{
		TextDocument: textDocumentIdentifier{URI: pathToURI(absPath)},
		Position:     Position{Line: line, Character: col},
	}, &locs)
	return locs, err
}

// References returns all references to the symbol at the given position.
func (c *Client) References(ctx context.Context, absPath string, line, col int) ([]Location, error) {
	var locs []Location
	err := c.call(ctx, "textDocument/references", referencesParams{
		TextDocument: textDocumentIdentifier{URI: pathToURI(absPath)},
		Position:     Position{Line: line, Character: col},
		Context:      referenceContext{IncludeDeclaration: true},
	}, &locs)
	return locs, err
}

// Hover returns hover documentation for the symbol at the given position.
func (c *Client) Hover(ctx context.Context, absPath string, line, col int) (string, error) {
	var result hoverResult
	if err := c.call(ctx, "textDocument/hover", textDocumentPositionParams{
		TextDocument: textDocumentIdentifier{URI: pathToURI(absPath)},
		Position:     Position{Line: line, Character: col},
	}, &result); err != nil {
		return "", err
	}
	return result.Contents.Value, nil
}

// Format requests that the server format the given file. Returns the edits to apply.
func (c *Client) Format(ctx context.Context, absPath string) ([]TextEdit, error) {
	var edits []TextEdit
	err := c.call(ctx, "textDocument/formatting", formattingParams{
		TextDocument: textDocumentIdentifier{URI: pathToURI(absPath)},
		Options:      formattingOptions{TabSize: 4, InsertSpaces: false},
	}, &edits)
	return edits, err
}

// --- internal ---

// initialize performs the LSP initialize handshake.
func (c *Client) initialize(ctx context.Context, workspaceDir string) error {
	rootURI := pathToURI(workspaceDir)
	params := initializeParams{
		ProcessID: os.Getpid(),
		RootURI:   rootURI,
		Capabilities: clientCapabilities{
			Workspace: workspaceClientCapabilities{ApplyEdit: false},
			TextDocument: textDocumentClientCapabilities{
				PublishDiagnostics: map[string]any{},
				Diagnostic:         map[string]any{"relatedDocumentSupport": false},
			},
		},
		WorkspaceFolders: []workspaceFolder{{URI: rootURI, Name: "workspace"}},
	}
	var result json.RawMessage
	if err := c.call(ctx, "initialize", params, &result); err != nil {
		return err
	}
	return c.notify("initialized", map[string]any{})
}

// call sends a JSON-RPC request and waits for the response.
func (c *Client) call(ctx context.Context, method string, params, result any) error {
	id := c.nextID.Add(1)
	ch := make(chan *response, 1)

	c.mu.Lock()
	c.pending[id] = ch
	if err := c.writeMessage(&request{JSONRPC: "2.0", ID: &id, Method: method, Params: params}); err != nil {
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("lsp call %s: write: %w", method, err)
	}
	c.mu.Unlock()

	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return ctx.Err()
	case <-c.done:
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("lsp: server stopped")
	case resp := <-ch:
		if resp.Error != nil {
			return resp.Error
		}
		if result != nil && len(resp.Result) > 0 {
			return json.Unmarshal(resp.Result, result)
		}
		return nil
	}
}

// notify sends a JSON-RPC notification (no response expected).
func (c *Client) notify(method string, params any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeMessage(&request{JSONRPC: "2.0", Method: method, Params: params})
}

// writeMessage encodes msg as JSON and writes it with Content-Length framing.
// Must be called with c.mu held.
func (c *Client) writeMessage(msg any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(data))
	if _, err := io.WriteString(c.stdin, header); err != nil {
		return err
	}
	_, err = c.stdin.Write(data)
	return err
}

// readLoop reads messages from the server stdout until the client is stopped.
func (c *Client) readLoop() {
	defer c.wg.Done()
	for {
		select {
		case <-c.done:
			return
		default:
		}

		data, err := c.readMessage()
		if err != nil {
			if errors.Is(err, io.EOF) || strings.Contains(err.Error(), "closed") {
				return
			}
			continue
		}

		var resp response
		if err := json.Unmarshal(data, &resp); err != nil {
			continue
		}

		// Server notification (no ID).
		if resp.ID == nil {
			c.handleNotification(&resp)
			continue
		}

		// Response to a pending request.
		c.mu.Lock()
		ch, ok := c.pending[*resp.ID]
		if ok {
			delete(c.pending, *resp.ID)
		}
		c.mu.Unlock()

		if ok {
			select {
			case ch <- &resp:
			default:
			}
		}
	}
}

// handleNotification processes push notifications from the server.
func (c *Client) handleNotification(resp *response) {
	if resp.Method == "textDocument/publishDiagnostics" {
		var p publishDiagnosticsParams
		if err := json.Unmarshal(resp.Params, &p); err == nil {
			c.diagMu.Lock()
			// Evict an arbitrary entry when the cache is full to bound memory use.
			if _, exists := c.diagMap[p.URI]; !exists && len(c.diagMap) >= diagMapMaxFiles {
				for k := range c.diagMap {
					delete(c.diagMap, k)
					break
				}
			}
			c.diagMap[p.URI] = p.Diagnostics
			c.diagMu.Unlock()
		}
	}
}

// readMessage reads one LSP message from the server stdout using Content-Length framing.
func (c *Client) readMessage() ([]byte, error) {
	var contentLen int
	for {
		line, err := c.stdout.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		if after, ok := strings.CutPrefix(line, "Content-Length:"); ok {
			n, err := strconv.Atoi(strings.TrimSpace(after))
			if err == nil {
				contentLen = n
			}
		}
	}
	if contentLen == 0 {
		return nil, fmt.Errorf("lsp: missing or zero Content-Length")
	}
	body := make([]byte, contentLen)
	_, err := io.ReadFull(c.stdout, body)
	return body, err
}
