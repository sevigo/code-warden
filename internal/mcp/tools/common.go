package tools

import (
	"context"
	"fmt"
)

// Input validation limits.
const (
	MaxQueryLength   = 10000
	MaxResultLimit   = 100
	MinResultLimit   = 1
	MaxTitleLength   = 500
	MaxBodyLength    = 65536 // GitHub limit is 64KB
	MaxSymbolLength  = 200
	MaxDirPathLength = 500
)

// contextKey is an unexported type for context keys in this package.
type contextKey string

const projectRootKey contextKey = "projectRoot"

// ProjectRootFromContext returns the per-session project root from context,
// or empty string if not set.
func ProjectRootFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(projectRootKey).(string); ok {
		return v
	}
	return ""
}

// WithProjectRoot returns a context with the given project root.
func WithProjectRoot(ctx context.Context, root string) context.Context {
	return context.WithValue(ctx, projectRootKey, root)
}

// GetRequiredString extracts a required string parameter from args.
// Returns an error if the parameter is missing or exceeds maxLength.
func GetRequiredString(args map[string]any, paramName string, maxLength int) (string, error) {
	val, ok := args[paramName].(string)
	if !ok || val == "" {
		return "", fmt.Errorf("%s is required", paramName)
	}
	if maxLength > 0 && len(val) > maxLength {
		return "", fmt.Errorf("%s exceeds maximum length of %d characters", paramName, maxLength)
	}
	return val, nil
}

// GetOptionalString extracts an optional string parameter from args.
// Returns empty string if not present.
func GetOptionalString(args map[string]any, paramName string) string {
	val, _ := args[paramName].(string)
	return val
}

// ParseLimit extracts a limit parameter from args with bounds checking.
// Returns defaultLimit if not present or invalid.
func ParseLimit(args map[string]any, defaultLimit int) int {
	limit := defaultLimit
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
		if limit < MinResultLimit {
			limit = MinResultLimit
		} else if limit > MaxResultLimit {
			limit = MaxResultLimit
		}
	}
	return limit
}
