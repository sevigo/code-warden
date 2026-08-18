// Package review implements the agent-based multi-angle code review runner
// that replaces the RAG retrieval pipeline. It dispatches parallel agent
// passes (bug, security, performance, conventions) that investigate the diff
// with grep + read_file against a local clone, then merges their findings.
package review

import (
	"github.com/sevigo/code-warden/internal/llm"
)

// Angle describes a single review lens (a system prompt) applied by one agent pass.
type Angle struct {
	// Name identifies the angle and is used as the agent task/loop ID.
	Name string

	// PromptKey is the PromptManager key for the angle's system prompt file.
	PromptKey llm.PromptKey

	// Description is a short human-readable summary of the angle.
	Description string
}

// DefaultAngles are the review lenses run in parallel by the runner.
// All use the same model; only the system prompt differs.
var DefaultAngles = []Angle{
	{Name: "bug", PromptKey: llm.ReviewBugPrompt, Description: "logic bugs and correctness"},
	{Name: "security", PromptKey: llm.ReviewSecurityPrompt, Description: "security vulnerabilities"},
	{Name: "performance", PromptKey: llm.ReviewPerformancePrompt, Description: "performance and scalability"},
	{Name: "conventions", PromptKey: llm.ReviewConventionsPrompt, Description: "conventions and maintainability"},
}
