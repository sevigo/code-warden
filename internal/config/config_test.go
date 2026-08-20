package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAgentConfigRejectsRemovedNativeMode(t *testing.T) {
	t.Parallel()

	cfg := validAgentConfig()
	cfg.Mode = "native"

	err := cfg.Validate()
	require.ErrorContains(t, err, "agent.mode must be 'warden' or 'pi'")
}

func TestAgentConfigAcceptsWardenMode(t *testing.T) {
	t.Parallel()

	cfg := validAgentConfig()
	cfg.WorkingDir = t.TempDir()
	err := cfg.Validate()
	require.NoError(t, err)
}

func validAgentConfig() AgentConfig {
	return AgentConfig{
		Enabled:               true,
		Mode:                  "warden",
		Model:                 "test-model",
		Timeout:               "1m",
		MaxIterations:         1,
		MaxConcurrentSessions: 1,
		InProcessOnly:         true,
	}
}
