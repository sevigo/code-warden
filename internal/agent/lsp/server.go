package lsp

// LanguageServer describes a language server binary and the file extensions it handles.
// Implement this interface to add support for a new language — register it with
// NewManager(workspace, MyServer{}, ...) or DefaultServers().
type LanguageServer interface {
	// Name returns a human-readable identifier, e.g. "go", "typescript".
	Name() string
	// Extensions returns the file suffixes this server handles, e.g. [".go"].
	Extensions() []string
	// Command returns the executable + arguments to launch the server.
	// workspaceDir is the agent workspace root (useful for some servers).
	Command(workspaceDir string) []string
	// Env returns any additional environment variables required by the server.
	// Return nil if none are needed.
	Env() []string
	// LanguageID returns the LSP language identifier for didOpen notifications,
	// e.g. "go", "typescript", "python".
	LanguageID() string
}

// DefaultServers returns the built-in language server implementations.
// Add new languages here as they are implemented.
func DefaultServers() []LanguageServer {
	return []LanguageServer{
		GoServer{},
		TypeScriptServer{},
		PythonServer{},
		RustServer{},
	}
}

// --- Go ---

// GoServer runs gopls, the official Go language server.
// Requires: gopls in PATH (go install golang.org/x/tools/gopls@latest).
type GoServer struct{}

func (GoServer) Name() string              { return "go" }
func (GoServer) Extensions() []string      { return []string{".go"} }
func (GoServer) LanguageID() string        { return "go" }
func (GoServer) Env() []string             { return nil }
func (GoServer) Command(_ string) []string { return []string{"gopls"} }

// --- TypeScript / JavaScript ---

// TypeScriptServer runs typescript-language-server.
// Requires: npm install -g typescript-language-server typescript
type TypeScriptServer struct{}

func (TypeScriptServer) Name() string         { return "typescript" }
func (TypeScriptServer) Extensions() []string { return []string{".ts", ".tsx", ".js", ".jsx", ".mjs"} }
func (TypeScriptServer) LanguageID() string   { return "typescript" }
func (TypeScriptServer) Env() []string        { return nil }
func (TypeScriptServer) Command(_ string) []string {
	return []string{"typescript-language-server", "--stdio"}
}

// --- Python ---

// PythonServer runs pylsp (Python LSP Server).
// Requires: pip install python-lsp-server
type PythonServer struct{}

func (PythonServer) Name() string              { return "python" }
func (PythonServer) Extensions() []string      { return []string{".py"} }
func (PythonServer) LanguageID() string        { return "python" }
func (PythonServer) Env() []string             { return nil }
func (PythonServer) Command(_ string) []string { return []string{"pylsp"} }

// --- Rust ---

// RustServer runs rust-analyzer.
// Requires: rustup component add rust-analyzer (or install from GitHub releases).
type RustServer struct{}

func (RustServer) Name() string              { return "rust" }
func (RustServer) Extensions() []string      { return []string{".rs"} }
func (RustServer) LanguageID() string        { return "rust" }
func (RustServer) Env() []string             { return nil }
func (RustServer) Command(_ string) []string { return []string{"rust-analyzer"} }
