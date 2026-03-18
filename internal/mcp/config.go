package mcp

// GovernanceConfig configures the governance layer for MCP tool execution.
// It provides security controls like permission checks and rate limiting.
type GovernanceConfig struct {
	// AllowedTools restricts which tools can be called.
	// If empty, all tools are allowed.
	AllowedTools []string `yaml:"allowed_tools"`

	// DeniedTools explicitly blocks certain tools.
	DeniedTools []string `yaml:"denied_tools"`

	// RateLimits maps tool names to max calls globally across all sessions.
	// Note: Rate limits are global across all sessions, not per-session.
	// Example: {"review_code": 5} limits review_code to 5 calls total across all sessions.
	RateLimits map[string]int `yaml:"rate_limits"`

	// EnableGovernance enables the governance layer.
	// If false, all tools are executed without validation.
	EnableGovernance bool `yaml:"enable_governance"`
}

// DefaultGovernanceConfig returns a default configuration with governance disabled.
func DefaultGovernanceConfig() GovernanceConfig {
	return GovernanceConfig{
		EnableGovernance: false,
	}
}
