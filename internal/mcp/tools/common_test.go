package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestProjectRootContext(t *testing.T) {
	ctx := context.Background()

	// Test empty context
	assert.Equal(t, "", ProjectRootFromContext(ctx))

	// Test with project root
	root := "/path/to/project"
	ctx = WithProjectRoot(ctx, root)
	assert.Equal(t, root, ProjectRootFromContext(ctx))
}

func TestInputValidationLimits(t *testing.T) {
	assert.Equal(t, 10000, MaxQueryLength)
	assert.Equal(t, 100, MaxResultLimit)
	assert.Equal(t, 1, MinResultLimit)
}
