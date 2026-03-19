package index

import (
	"context"
	"log/slog"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sevigo/goframe/parsers"
	"github.com/sevigo/goframe/schema"
)

// File extensions for supported languages.
const (
	extGo         = ".go"
	extTypeScript = ".ts"
	extTSX        = ".tsx"
	extPython     = ".py"
	extJavaScript = ".js"
	extJSX        = ".jsx"
	extJava       = ".java"
	extRust       = ".rs"
)

// DefinitionExtractor extracts type/func/interface definitions from source files.
type DefinitionExtractor struct {
	parserRegistry parsers.ParserRegistry
	logger         *slog.Logger
}

// NewDefinitionExtractor creates a new DefinitionExtractor.
func NewDefinitionExtractor(parserRegistry parsers.ParserRegistry, logger *slog.Logger) *DefinitionExtractor {
	return &DefinitionExtractor{
		parserRegistry: parserRegistry,
		logger:         logger,
	}
}

// ExtractDefinitions extracts type, function, and interface definitions from a file.
// Returns documents with chunk_type="definition" for semantic search.
func (d *DefinitionExtractor) ExtractDefinitions(_ context.Context, fullPath, relPath string, content []byte) []schema.Document {
	ext := strings.ToLower(filepath.Ext(fullPath))

	// Try parser-based extraction first
	if d.parserRegistry != nil {
		parser, err := d.parserRegistry.GetParserForExtension(ext)
		if err == nil {
			defs := d.extractWithParser(parser, fullPath, relPath, content)
			if len(defs) > 0 {
				return defs
			}
		}
	}

	// Fallback to regex-based extraction
	return d.extractWithRegex(fullPath, relPath, content)
}

// extractWithParser uses goframe parsers to extract definitions.
func (d *DefinitionExtractor) extractWithParser(parser schema.ParserPlugin, fullPath, relPath string, content []byte) []schema.Document {
	// Extract metadata including definitions
	metadata, err := parser.ExtractMetadata(string(content), fullPath)
	if err != nil {
		d.logger.Debug("parser metadata extraction failed, using regex fallback", "file", relPath, "error", err)
		return nil
	}

	if len(metadata.Definitions) == 0 {
		return nil
	}

	var docs []schema.Document
	for _, def := range metadata.Definitions {
		if def.Name == "" || def.Type == "" {
			continue
		}

		// Only extract exported/public symbols
		if def.Visibility != "public" && !isExported(def.Name, filepath.Ext(fullPath)) {
			continue
		}

		// Build the definition content from signature and documentation.
		// Prepending the doc comment means the embedding captures both the
		// natural-language description (what it does) and the code signature
		// (how to use it), improving semantic retrieval significantly.
		var content strings.Builder
		if def.Documentation != "" {
			content.WriteString(def.Documentation)
			content.WriteString("\n\n")
		}
		content.WriteString(def.Signature)

		doc := schema.NewDocument(content.String(), map[string]any{
			"chunk_type":    "definition",
			"source":        relPath,
			"line":          def.LineStart,
			"end_line":      def.LineEnd,
			"identifier":    def.Name,
			"kind":          def.Type,
			"package_name":  metadata.PackageName,
			"is_exported":   def.Visibility == "public",
			"signature":     def.Signature,
			"documentation": def.Documentation,
		})

		docs = append(docs, doc)
	}

	return docs
}

// extractWithRegex uses regex patterns to extract definitions.
func (d *DefinitionExtractor) extractWithRegex(fullPath, relPath string, content []byte) []schema.Document {
	ext := strings.ToLower(filepath.Ext(fullPath))
	strContent := string(content)

	var patterns []*definitionPattern
	switch ext {
	case extGo:
		patterns = goDefinitionPatterns
	case extTypeScript, extTSX:
		patterns = typeScriptDefinitionPatterns
	case extPython:
		patterns = pythonDefinitionPatterns
	case extJavaScript, extJSX:
		patterns = javaScriptDefinitionPatterns
	case extJava:
		patterns = javaDefinitionPatterns
	case extRust:
		patterns = rustDefinitionPatterns
	default:
		return nil
	}

	var docs []schema.Document
	seen := make(map[string]bool)

	for _, pattern := range patterns {
		matches := pattern.regex.FindAllStringSubmatchIndex(strContent, -1)
		for _, match := range matches {
			if len(match) < 4 {
				continue
			}

			start := match[0]
			end := match[1]
			nameStart := match[2]
			nameEnd := match[3]

			name := strContent[nameStart:nameEnd]
			if seen[name] {
				continue
			}
			seen[name] = true

			// Check if exported (Go convention)
			if ext == extGo && !isExported(name, ext) {
				continue
			}

			docComment := extractDocComment(strContent, start, ext)
			definition := extractCompleteDefinition(strContent, start, end, pattern.kind)

			// Prepend doc comment so the embedding captures both description and code.
			var defContent strings.Builder
			if docComment != "" {
				defContent.WriteString(docComment)
				defContent.WriteString("\n")
			}
			defContent.WriteString(definition)

			line := countLines(strContent[:start])

			doc := schema.NewDocument(defContent.String(), map[string]any{
				"chunk_type":    "definition",
				"source":        relPath,
				"line":          line,
				"identifier":    name,
				"kind":          pattern.kind,
				"package_name":  extractPackageName(strContent),
				"is_exported":   ext != extGo || isExported(name, ext),
				"documentation": docComment,
			})

			docs = append(docs, doc)
		}
	}

	return docs
}

type definitionPattern struct {
	regex *regexp.Regexp
	kind  string
}

var goDefinitionPatterns = []*definitionPattern{
	// type Name struct { ... }
	{regexp.MustCompile(`(?m)type\s+([A-Z][a-zA-Z0-9]*)\s+struct\s*\{`), "struct"},
	// type Name interface { ... }
	{regexp.MustCompile(`(?m)type\s+([A-Z][a-zA-Z0-9]*)\s+interface\s*\{`), "interface"},
	// type Name = Type or type Name Type
	{regexp.MustCompile(`(?m)type\s+([A-Z][a-zA-Z0-9]*)\s+(?:=)?\s*[A-Z]`), "type"},
	// func Name(...) or func (receiver) Name(...)
	{regexp.MustCompile(`(?m)func\s+(?:\([^)]+\)\s+)?([A-Z][a-zA-Z0-9]*)\s*\(`), "func"},
	// const Name = value
	{regexp.MustCompile(`(?m)const\s+([A-Z][a-zA-Z0-9]*)\s*=`), "const"},
	// var Name = value (only exported)
	{regexp.MustCompile(`(?m)var\s+([A-Z][a-zA-Z0-9]*)\s*[=]`), "var"},
}

var typeScriptDefinitionPatterns = []*definitionPattern{
	// interface Name { ... }
	{regexp.MustCompile(`(?m)interface\s+([A-Z][a-zA-Z0-9]*)\s*\{`), "interface"},
	// class Name { ... }
	{regexp.MustCompile(`(?m)class\s+([A-Z][a-zA-Z0-9]*)\s*(?:extends|implements|\{)`), "class"},
	// type Name = ...
	{regexp.MustCompile(`(?m)type\s+([A-Z][a-zA-Z0-9]*)\s*=`), "type"},
	// function Name(...) or const Name = (...) =>
	{regexp.MustCompile(`(?m)(?:function|const)\s+([A-Z][a-zA-Z0-9]*)\s*[\(=]`), "func"},
	// export function/class/type
	{regexp.MustCompile(`(?m)export\s+(?:function|class|type|interface)\s+([A-Z][a-zA-Z0-9]*)`), "export"},
}

var pythonDefinitionPatterns = []*definitionPattern{
	// class Name(...):
	{regexp.MustCompile(`(?m)class\s+([A-Z][a-zA-Z0-9]*)\s*[:\(]`), "class"},
	// def name(...): (include all, Python doesn't have export concept)
	{regexp.MustCompile(`(?m)def\s+([a-zA-Z_][a-zA-Z0-9_]*)\s*\(`), "func"},
}

var javaScriptDefinitionPatterns = []*definitionPattern{
	// class Name { ... }
	{regexp.MustCompile(`(?m)class\s+([A-Z][a-zA-Z0-9]*)\s*(?:extends|\{)`), "class"},
	// function Name(...) { ... }
	{regexp.MustCompile(`(?m)function\s+([A-Z][a-zA-Z0-9]*)\s*\(`), "func"},
	// const Name = ... or export
	{regexp.MustCompile(`(?m)(?:export\s+)?(?:const|let|var)\s+([A-Z][a-zA-Z0-9]*)\s*=`), "const"},
}

var javaDefinitionPatterns = []*definitionPattern{
	// class Name { ... }
	{regexp.MustCompile(`(?m)(?:public|private|protected)?\s*class\s+([A-Z][a-zA-Z0-9]*)\s*(?:extends|implements|\{)`), "class"},
	// interface Name { ... }
	{regexp.MustCompile(`(?m)interface\s+([A-Z][a-zA-Z0-9]*)\s*\{`), "interface"},
	// method definition
	{regexp.MustCompile(`(?m)(?:public|private|protected)\s+(?:static\s+)?[a-zA-Z<>]+\s+([a-zA-Z0-9]+)\s*\(`), "method"},
}

var rustDefinitionPatterns = []*definitionPattern{
	// struct Name { ... }
	{regexp.MustCompile(`(?m)struct\s+([A-Z][a-zA-Z0-9]*)\s*(?:\{|where|\n)`), "struct"},
	// enum Name { ... }
	{regexp.MustCompile(`(?m)enum\s+([A-Z][a-zA-Z0-9]*)\s*\{`), "enum"},
	// trait Name { ... }
	{regexp.MustCompile(`(?m)trait\s+([A-Z][a-zA-Z0-9]*)\s*\{`), "trait"},
	// fn name(...) or pub fn name(...)
	{regexp.MustCompile(`(?m)(?:pub\s+)?fn\s+([a-zA-Z0-9_]+)\s*\(`), "func"},
	// type Name = ... or pub type Name
	{regexp.MustCompile(`(?m)(?:pub\s+)?type\s+([A-Z][a-zA-Z0-9]*)\s*=`), "type"},
}

// countLines returns the 1-based line number for a byte offset.
func countLines(s string) int {
	if s == "" {
		return 1
	}
	lines := 1
	for _, c := range s {
		if c == '\n' {
			lines++
		}
	}
	return lines
}

// extractCompleteDefinition extends the match to include the full definition body.
//
//nolint:gocognit
func extractCompleteDefinition(content string, start, end int, kind string) string {
	// For type definitions, try to include the full body
	if kind == "struct" || kind == "interface" || kind == "class" || kind == "type" {
		// Find opening brace
		braceStart := strings.Index(content[start:], "{")
		if braceStart == -1 {
			return content[start:end]
		}
		braceStart += start

		// Find matching closing brace
		depth := 1
		i := braceStart + 1
		for i < len(content) && depth > 0 {
			switch content[i] {
			case '{':
				depth++
			case '}':
				depth--
			}
			i++
		}

		if depth == 0 {
			// Include some context before the definition
			contextStart := max(0, start-50)
			return strings.TrimSpace(content[contextStart:i])
		}
	}

	// For functions, include the signature
	if kind == "func" || kind == "method" {
		// Find end of signature (opening brace or newline)
		endIdx := end
		for endIdx < len(content) && content[endIdx] != '{' && content[endIdx] != '\n' {
			endIdx++
		}
		if endIdx < len(content) && content[endIdx] == '{' {
			// Include a reasonable portion of the body (up to 5 lines or closing brace)
			lines := 0
			bodyEnd := endIdx
			for bodyEnd < len(content) && lines < 5 {
				if content[bodyEnd] == '\n' {
					lines++
				}
				bodyEnd++
			}
			return strings.TrimSpace(content[start:bodyEnd])
		}
		return strings.TrimSpace(content[start:endIdx])
	}

	return strings.TrimSpace(content[start:end])
}

// extractDocComment walks backwards from byteOffset in content to collect the
// comment block immediately preceding a definition. Returns an empty string if
// no comment is found.  Language-specific comment prefixes are used so that
// Go `//`, JSDoc `/** */`, Python `#`, Rust `///`, and Java `/** */` are all
// handled correctly.
func extractDocComment(content string, byteOffset int, ext string) string {
	lines := strings.Split(content[:byteOffset], "\n")

	var commentLines []string
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			// A blank line separating comments from the definition — stop.
			if len(commentLines) > 0 {
				break
			}
			continue
		}

		isComment := false
		switch ext {
		case extGo:
			isComment = strings.HasPrefix(trimmed, "//")
		case extTypeScript, extTSX, extJavaScript, extJSX:
			isComment = strings.HasPrefix(trimmed, "//") ||
				strings.HasPrefix(trimmed, "*") ||
				strings.HasPrefix(trimmed, "/*")
		case extPython:
			isComment = strings.HasPrefix(trimmed, "#")
		case extJava:
			isComment = strings.HasPrefix(trimmed, "//") ||
				strings.HasPrefix(trimmed, "*") ||
				strings.HasPrefix(trimmed, "/*")
		case extRust:
			isComment = strings.HasPrefix(trimmed, "///") || strings.HasPrefix(trimmed, "//")
		}

		if !isComment {
			break
		}
		commentLines = append([]string{line}, commentLines...)
	}

	return strings.TrimSpace(strings.Join(commentLines, "\n"))
}

// extractPackageName attempts to extract the package name from file content.
func extractPackageName(content string) string {
	// Go: package name
	goPackage := regexp.MustCompile(`(?m)^package\s+([a-zA-Z_][a-zA-Z0-9_]*)`)
	if match := goPackage.FindStringSubmatch(content); match != nil {
		return match[1]
	}

	// TypeScript/JavaScript: export or from
	// Python: module handling
	// Java: package statement

	return ""
}

// isExported checks if a symbol is exported based on language conventions.
func isExported(name, ext string) bool {
	if name == "" {
		return false
	}

	switch ext {
	case extGo:
		// Go: uppercase first letter means exported
		return name[0] >= 'A' && name[0] <= 'Z'
	case extJava:
		// Java: typically PascalCase for public
		return name[0] >= 'A' && name[0] <= 'Z'
	case extPython:
		// Python: no real private, but __ prefix is convention
		return !strings.HasPrefix(name, "__")
	case extTypeScript, extTSX, extJavaScript, extJSX:
		// JS/TS: typically PascalCase for exported
		return len(name) > 0 && name[0] >= 'A' && name[0] <= 'Z'
	case extRust:
		// Rust: PascalCase for types, snake_case for functions
		return len(name) > 0 && name[0] >= 'A' && name[0] <= 'Z'
	default:
		return true
	}
}
