package tools

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/sevigo/goframe/vectorstores"

	"github.com/sevigo/code-warden/internal/storage"
)

// GetCallers finds all functions that call the specified function.
type GetCallers struct {
	VectorStore storage.ScopedVectorStore
	Logger      *slog.Logger
}

// CalleeLocation represents a single caller location.
type CalleeLocation struct {
	File       string  `json:"file"`
	Line       int     `json:"line"`
	CallerName string  `json:"caller_name"`
	Context    string  `json:"context"`
	Score      float64 `json:"score"`
}

// CallersResponse is the response for get_callers tool.
type CallersResponse struct {
	Function string           `json:"function"`
	Count    int              `json:"count"`
	Callers  []CalleeLocation `json:"callers,omitempty"`
	Message  string           `json:"message,omitempty"`
}

func (t *GetCallers) Name() string {
	return "get_callers"
}

func (t *GetCallers) Description() string {
	return `Find all functions that call the specified function.
Returns locations where the function is invoked.
Use this to understand the impact of changes to a function.`
}

func (t *GetCallers) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"function": map[string]any{
				"type":        "string",
				"description": "The function name to find callers for (e.g., 'GenerateReview', 'ProcessFile')",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum number of results (default: 20)",
				"default":     20,
			},
		},
		"required": []string{"function"},
	}
}

func (t *GetCallers) Execute(ctx context.Context, args map[string]any) (any, error) {
	function, ok := args["function"].(string)
	if !ok || function == "" {
		return nil, fmt.Errorf("function is required")
	}
	t.Logger.Info("get_callers: executing tool", "function", function)

	limit := 20
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
		if limit < 1 {
			limit = 1
		} else if limit > MaxResultLimit {
			limit = MaxResultLimit
		}
	}

	// Search for call sites: "function(" pattern
	query := fmt.Sprintf("%s( call invocation", function)

	docsWithScores, err := t.VectorStore.SimilaritySearchWithScores(ctx, query, limit*2,
		vectorstores.WithFilters(map[string]any{
			"chunk_type": "code",
		}),
	)
	if err != nil {
		t.Logger.Error("get_callers: search failed", "function", function, "error", err)
		return nil, fmt.Errorf("search failed: %w", err)
	}

	var callers []CalleeLocation
	seenCallers := make(map[string]bool)

	for _, doc := range docsWithScores {
		content := doc.Document.PageContent

		// Check if this looks like a call site: functionName( pattern
		if !isCallSite(content, function) {
			continue
		}

		source, _ := doc.Document.Metadata["source"].(string)
		line, _ := doc.Document.Metadata["line"].(int)

		// Extract caller function name from context
		callerName := extractCallerFunction(content, function)

		// Deduplicate by file+caller
		key := source + ":" + callerName
		if seenCallers[key] {
			continue
		}
		seenCallers[key] = true

		callers = append(callers, CalleeLocation{
			File:       source,
			Line:       line,
			CallerName: callerName,
			Context:    extractCallContext(content, function),
			Score:      float64(doc.Score),
		})

		if len(callers) >= limit {
			break
		}
	}

	if len(callers) == 0 {
		return CallersResponse{
			Function: function,
			Count:    0,
			Message:  fmt.Sprintf("No callers found for function '%s'", function),
		}, nil
	}

	return CallersResponse{
		Function: function,
		Count:    len(callers),
		Callers:  callers,
	}, nil
}

// isCallSite checks if content contains a function call pattern.
func isCallSite(content, function string) bool {
	// Look for function( or .function( patterns
	contentLower := strings.ToLower(content)
	functionLower := strings.ToLower(function)

	// Pattern: functionName( or .functionName(
	if strings.Contains(contentLower, functionLower+"(") {
		return true
	}
	if strings.Contains(contentLower, "."+functionLower+"(") {
		return true
	}
	return false
}

// extractCallerFunction attempts to find the containing function name.
func extractCallerFunction(content, _ string) string {
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		// Look for function definition pattern
		if strings.Contains(line, "func ") && strings.Contains(line, "(") {
			return extractFunctionNameFromLine(line)
		}
	}
	return "<unknown>"
}

// extractFunctionNameFromLine extracts the function name from a declaration line.
func extractFunctionNameFromLine(line string) string {
	parts := strings.Split(line, "func ")
	if len(parts) > 1 {
		funcPart := strings.TrimSpace(parts[1])
		// Handle "func (receiver) Name(" pattern
		if strings.HasPrefix(funcPart, "(") {
			// Method on a type
			if idx := strings.Index(funcPart, ") "); idx != -1 {
				methodPart := funcPart[idx+2:]
				return extractFunctionNameFromSignature(methodPart)
			}
		}
		return extractFunctionNameFromSignature(funcPart)
	}
	return "<unknown>"
}

// extractFunctionNameFromSignature extracts the function name from a signature.
func extractFunctionNameFromSignature(s string) string {
	// Find the first ( for parameters
	idx := strings.Index(s, "(")
	if idx > 0 {
		return strings.TrimSpace(s[:idx])
	}
	// Find space before return type
	if idx = strings.Index(s, " "); idx > 0 {
		return strings.TrimSpace(s[:idx])
	}
	return s
}

// extractCallContext returns lines around the call site.
func extractCallContext(content, function string) string {
	lines := strings.Split(content, "\n")
	var contextLines []string

	for i, line := range lines {
		if strings.Contains(line, function+"(") {
			start := max(0, i-2)
			end := min(len(lines), i+3)
			contextLines = lines[start:end]
			break
		}
	}

	if len(contextLines) == 0 {
		if len(content) > 200 {
			return content[:200] + "..."
		}
		return content
	}

	return strings.Join(contextLines, "\n")
}
