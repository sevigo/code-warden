package agent

import (
	"github.com/sevigo/code-warden/internal/agent/reviewtools"
	"github.com/sevigo/code-warden/internal/mcp"
)

// searchTools returns the read-only workspace search tools used by the
// implementation planner. Review workflows construct these tools directly
// from the review-owned package.
func searchTools(projectRoot string) []mcp.Tool {
	return []mcp.Tool{
		reviewtools.NewGrep(projectRoot),
		reviewtools.NewFind(projectRoot),
	}
}
