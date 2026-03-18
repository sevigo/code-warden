package tools

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/sevigo/goframe/vectorstores"

	"github.com/sevigo/code-warden/internal/storage"
)

// GetCallees finds all functions called by the specified function.
type GetCallees struct {
	VectorStore storage.ScopedVectorStore
	Logger      *slog.Logger
}

// CalleeInfo represents a called function.
type CalleeInfo struct {
	Name      string `json:"name"`
	File      string `json:"file,omitempty"`
	Line      int    `json:"line,omitempty"`
	IsBuiltin bool   `json:"is_builtin"`
	Count     int    `json:"count"`
}

// CalleesResponse is the response for get_callees tool.
type CalleesResponse struct {
	Function string       `json:"function"`
	Count    int          `json:"count"`
	Callees  []CalleeInfo `json:"callees,omitempty"`
	Message  string       `json:"message,omitempty"`
}

func (t *GetCallees) Name() string {
	return "get_callees"
}

func (t *GetCallees) Description() string {
	return `Find all functions called by the specified function.
Returns a list of functions invoked within the function body.
Use this to understand dependencies and potential impact of changes.`
}

func (t *GetCallees) ParametersSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"function": map[string]any{
				"type":        "string",
				"description": "The function name to analyze (e.g., 'GenerateReview', 'ProcessFile')",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum number of results (default: 30)",
				"default":     30,
			},
		},
		"required": []string{"function"},
	}
}

// Common built-in functions that should be filtered
var builtinFunctions = map[string]bool{
	// Go builtins
	"make": true, "new": true, "len": true, "cap": true, "append": true,
	"copy": true, "delete": true, "close": true, "panic": true, "recover": true,
	"print": true, "println": true, "complex": true, "real": true, "imag": true,
	// Common standard library
	"fmt.Println": true, "fmt.Printf": true, "fmt.Sprintf": true,
	"fmt.Errorf":       true,
	"strings.Contains": true, "strings.HasPrefix": true, "strings.HasSuffix": true,
	"strings.Split": true, "strings.Join": true, "strings.TrimSpace": true,
	"errors.New":         true,
	"context.Background": true, "context.TODO": true,
	// Type conversions
	"string": true, "int": true, "int64": true, "float64": true, "bool": true,
}

func (t *GetCallees) Execute(ctx context.Context, args map[string]any) (any, error) { //nolint:gocognit // Complex by nature: multiple filtering stages
	function, ok := args["function"].(string)
	if !ok || function == "" {
		return nil, fmt.Errorf("function is required")
	}
	t.Logger.Info("get_callees: executing tool", "function", function)

	limit := 30
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
	}

	// First, get the function definition to analyze its body
	defQuery := fmt.Sprintf("definition of %s function", function)
	defDocs, err := t.VectorStore.SimilaritySearch(ctx, defQuery, 3,
		vectorstores.WithFilters(map[string]any{
			"chunk_type": "definition",
		}),
	)
	if err != nil {
		t.Logger.Error("get_callees: definition search failed", "function", function, "error", err)
		return nil, fmt.Errorf("search failed: %w", err)
	}

	if len(defDocs) == 0 {
		return CalleesResponse{
			Function: function,
			Count:    0,
			Message:  fmt.Sprintf("Function '%s' not found", function),
		}, nil
	}

	// Extract function calls from the definition content
	calleeMap := make(map[string]*CalleeInfo)

	for _, doc := range defDocs {
		content := doc.PageContent
		callees := extractFunctionCalls(content, function)

		for _, callee := range callees {
			// Skip built-in functions
			if builtinFunctions[callee] || builtinFunctions[strings.ToLower(callee)] {
				if calleeMap[callee] == nil {
					calleeMap[callee] = &CalleeInfo{
						Name:      callee,
						IsBuiltin: true,
						Count:     0,
					}
				}
				calleeMap[callee].Count++
				continue
			}

			// Skip method calls on variables (e.g., "s.Printf" - not a real function)
			if strings.Contains(callee, ".") && !strings.Contains(callee, "fmt.") && !strings.Contains(callee, "strings.") {
				// This might be a method call
				parts := strings.Split(callee, ".")
				if len(parts) == 2 {
					// Keep the method name only if it looks like an important call
					callee = parts[1]
				}
			}

			if calleeMap[callee] == nil {
				calleeMap[callee] = &CalleeInfo{
					Name:      callee,
					IsBuiltin: false,
					Count:     0,
				}
			}
			calleeMap[callee].Count++
		}
	}

	// Convert to sorted slice
	var callees []CalleeInfo
	for _, info := range calleeMap {
		// Skip very short names (likely noise)
		if len(info.Name) < 2 {
			continue
		}
		callees = append(callees, *info)
	}

	// Sort by count (most called first)
	// Limit results
	if len(callees) > limit {
		callees = callees[:limit]
	}

	if len(callees) == 0 {
		return CalleesResponse{
			Function: function,
			Count:    0,
			Message:  fmt.Sprintf("No callees found for function '%s'", function),
		}, nil
	}

	return CalleesResponse{
		Function: function,
		Count:    len(callees),
		Callees:  callees,
	}, nil
}

// extractFunctionCalls extracts function calls from code content.
//
//nolint:gocognit
func extractFunctionCalls(content, parentFunction string) []string {
	var calls []string
	seen := make(map[string]bool)

	// Pattern 1: FunctionName(args)
	// Matches: FunctionName(, pkg.FunctionName(, &Type{ (constructor)
	funcCallPattern := regexp.MustCompile(`\b([A-Z][a-zA-Z0-9_]*(?:\.[A-Z][a-zA-Z0-9_]*)?)\s*\(`)

	matches := funcCallPattern.FindAllStringSubmatch(content, -1)
	for _, match := range matches {
		if len(match) > 1 {
			name := match[1]
			// Skip the parent function itself
			if name == parentFunction {
				continue
			}
			if !seen[name] {
				seen[name] = true
				calls = append(calls, name)
			}
		}
	}

	// Pattern 2: Method calls - .MethodName(
	// This captures calls like: err.Method(, ctx.Method(
	methodPattern := regexp.MustCompile(`\.\s*([A-Z][a-zA-Z0-9_]*)\s*\(`)
	methodMatches := methodPattern.FindAllStringSubmatch(content, -1)
	for _, match := range methodMatches {
		if len(match) > 1 {
			method := match[1]
			if method == parentFunction {
				continue
			}
			if !seen[method] && len(method) >= 2 {
				seen[method] = true
				calls = append(calls, method)
			}
		}
	}

	// Pattern 3: Built-in calls like make(, new(, len(
	builtinPattern := regexp.MustCompile(`\b(make|new|len|cap|append|copy|delete|close|panic|recover)\s*\(`)
	builtinMatches := builtinPattern.FindAllStringSubmatch(content, -1)
	for _, match := range builtinMatches {
		if len(match) > 1 {
			name := match[1]
			if !seen[name] {
				seen[name] = true
				calls = append(calls, name)
			}
		}
	}

	return calls
}
