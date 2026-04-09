// Package lsp provides a Language Server Protocol client for agent workspaces.
// It supports multiple language servers simultaneously and is designed to be
// extended with new languages by implementing the LanguageServer interface.
package lsp

import (
	"fmt"
	"strings"
)

// --- JSON-RPC envelope ---

type request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int64 `json:"id,omitempty"` // nil for notifications
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"` // for notifications
	Result  jsonRaw         `json:"result,omitempty"`
	Error   *responseError  `json:"error,omitempty"`
	Params  jsonRaw         `json:"params,omitempty"` // for notifications
}

type responseError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *responseError) Error() string { return fmt.Sprintf("LSP error %d: %s", e.Code, e.Message) }

// jsonRaw is a type alias for raw JSON bytes (avoids importing encoding/json here).
type jsonRaw = []byte

// --- Core LSP types ---

// Position is a zero-based line and character offset within a text document.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range is a start/end pair of positions.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Location is a file URI + range pair.
type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

// DiagnosticSeverity mirrors the LSP severity enum.
type DiagnosticSeverity int

const (
	SeverityError       DiagnosticSeverity = 1
	SeverityWarning     DiagnosticSeverity = 2
	SeverityInformation DiagnosticSeverity = 3
	SeverityHint        DiagnosticSeverity = 4
)

func (s DiagnosticSeverity) String() string {
	switch s {
	case SeverityError:
		return "error"
	case SeverityWarning:
		return "warning"
	case SeverityInformation:
		return "info"
	case SeverityHint:
		return "hint"
	default:
		return "unknown"
	}
}

// Diagnostic is a compiler error, warning or hint for a specific range.
type Diagnostic struct {
	Range    Range              `json:"range"`
	Severity DiagnosticSeverity `json:"severity"`
	Message  string             `json:"message"`
	Source   string             `json:"source,omitempty"`
	Code     any                `json:"code,omitempty"`
}

// TextEdit is a single text replacement within a document.
type TextEdit struct {
	Range   Range  `json:"range"`
	NewText string `json:"newText"`
}

// --- LSP method param/result types ---

type textDocumentIdentifier struct {
	URI string `json:"uri"`
}

type versionedTextDocumentIdentifier struct {
	URI     string `json:"uri"`
	Version int    `json:"version"`
}

type textDocumentItem struct {
	URI        string `json:"uri"`
	LanguageID string `json:"languageId"`
	Version    int    `json:"version"`
	Text       string `json:"text"`
}

type textDocumentContentChangeEvent struct {
	Text string `json:"text"` // full-document sync
}

type didOpenParams struct {
	TextDocument textDocumentItem `json:"textDocument"`
}

type didChangeParams struct {
	TextDocument   versionedTextDocumentIdentifier  `json:"textDocument"`
	ContentChanges []textDocumentContentChangeEvent `json:"contentChanges"`
}

type textDocumentPositionParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

type referenceContext struct {
	IncludeDeclaration bool `json:"includeDeclaration"`
}

type referencesParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
	Context      referenceContext       `json:"context"`
}

type documentDiagnosticParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
}

type documentDiagnosticReport struct {
	Kind  string       `json:"kind"`  // "full" or "unchanged"
	Items []Diagnostic `json:"items"` // when kind == "full"
}

// publishDiagnosticsParams is received as a server notification.
type publishDiagnosticsParams struct {
	URI         string       `json:"uri"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// hoverResult is the result of textDocument/hover.
type hoverResult struct {
	Contents hoverContents `json:"contents"`
}

type hoverContents struct {
	Kind  string `json:"kind,omitempty"`
	Value string `json:"value,omitempty"`
	// MarkupContent or plain string
}

// formattingParams for textDocument/formatting.
type formattingParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
	Options      formattingOptions      `json:"options"`
}

type formattingOptions struct {
	TabSize      int  `json:"tabSize"`
	InsertSpaces bool `json:"insertSpaces"`
}

// workspaceClientCapabilities represents minimal client capabilities.
type workspaceClientCapabilities struct {
	ApplyEdit bool `json:"applyEdit"`
}

type textDocumentClientCapabilities struct {
	PublishDiagnostics map[string]any `json:"publishDiagnostics"`
	Diagnostic         map[string]any `json:"diagnostic"`
}

type clientCapabilities struct {
	Workspace    workspaceClientCapabilities    `json:"workspace"`
	TextDocument textDocumentClientCapabilities `json:"textDocument"`
}

type initializeParams struct {
	ProcessID        int                `json:"processId"`
	RootURI          string             `json:"rootUri"`
	Capabilities     clientCapabilities `json:"capabilities"`
	WorkspaceFolders []workspaceFolder  `json:"workspaceFolders"`
}

type workspaceFolder struct {
	URI  string `json:"uri"`
	Name string `json:"name"`
}

// --- URI helpers ---

// pathToURI converts an absolute filesystem path to a file:// URI.
func pathToURI(absPath string) string {
	if !strings.HasPrefix(absPath, "/") {
		absPath = "/" + absPath
	}
	return "file://" + absPath
}

// uriToPath converts a file:// URI back to an absolute filesystem path.
func uriToPath(uri string) string {
	return strings.TrimPrefix(uri, "file://")
}
