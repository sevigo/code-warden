package agent

import (
	goframeagent "github.com/sevigo/goframe/agent"

	"github.com/sevigo/code-warden/internal/mcp"
)

// ReadOnlyReviewTools returns the read-only investigation tools (grep, find,
// read_file, list_dir) wired to the given workspace root. Each tool is
// wrapped to inject the project root into its context. These are the only
// tools an agent-based review pass should have — no write/edit access.
func ReadOnlyReviewTools(projectRoot string) []goframeagent.Tool {
	base := []mcp.Tool{
		newGrepTool(),
		&findTool{},
		&readFileTool{},
		&listDirTool{},
	}

	tools := make([]goframeagent.Tool, 0, len(base))
	for _, t := range base {
		tools = append(tools, &contextInjectingTool{
			inner:       t,
			projectRoot: projectRoot,
		})
	}
	return tools
}
