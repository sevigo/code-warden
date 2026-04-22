package agent

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// crawlProjectContext scans the workspace for agent instruction files and
// skill definitions, returning a formatted markdown string to inject into
// the system prompt. It checks for:
//   - AGENTS.md or .code-warden/SYSTEM.md at the repo root
//   - .code-warden/skills/*.md (workflow definitions)
//
// This follows the emerging convention for repository-local agent
// instructions, giving repo owners a natural way to steer agent behavior
// without modifying the server deployment.
func crawlProjectContext(workspaceDir string) string {
	var sections []string

	// Check for AGENTS.md at repo root (standard agent convention)
	if agents, err := readMarkdownFile(filepath.Join(workspaceDir, "AGENTS.md")); err == nil && agents != "" {
		sections = append(sections, fmt.Sprintf("## AGENTS.md\n\n%s", agents))
	}

	// Check for .code-warden/SYSTEM.md (code-warden specific)
	if sysMD, err := readMarkdownFile(filepath.Join(workspaceDir, ".code-warden", "SYSTEM.md")); err == nil && sysMD != "" {
		sections = append(sections, fmt.Sprintf("## System Instructions\n\n%s", sysMD))
	}

	// Check for .code-warden/skills/*.md (workflow definitions)
	skillsDir := filepath.Join(workspaceDir, ".code-warden", "skills")
	if entries, err := os.ReadDir(skillsDir); err == nil {
		var skillSections []string
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			content, err := readMarkdownFile(filepath.Join(skillsDir, entry.Name()))
			if err != nil {
				slog.Warn("context_crawler: failed to read skill file", "path", filepath.Join(skillsDir, entry.Name()), "error", err)
				continue
			}
			if content == "" {
				continue
			}
			name := strings.TrimSuffix(entry.Name(), ".md")
			skillSections = append(skillSections, fmt.Sprintf("### Skill: %s\n\n%s", name, content))
		}
		if len(skillSections) > 0 {
			sections = append(sections, fmt.Sprintf("## Skills\n\n%s", strings.Join(skillSections, "\n\n")))
		}
	}

	if len(sections) == 0 {
		return ""
	}
	return strings.Join(sections, "\n\n")
}

// readMarkdownFile reads a markdown file and returns its trimmed content.
// Returns an empty string and nil error if the file exists but is empty.
func readMarkdownFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return "", nil
	}
	return content, nil
}
