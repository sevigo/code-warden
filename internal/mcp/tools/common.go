package tools

import (
	"context"
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
