package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDefaultRepoConfig(t *testing.T) {
	cfg := DefaultRepoConfig()

	assert.NotNil(t, cfg)
	assert.Empty(t, cfg.CustomInstructions)
	assert.Empty(t, cfg.ExcludeDirs)
	assert.Empty(t, cfg.ExcludeExts)
	assert.Empty(t, cfg.ExcludeFiles)
}
