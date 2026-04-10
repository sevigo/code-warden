package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCrawlProjectContext_NoFiles(t *testing.T) {
	dir := t.TempDir()
	got := crawlProjectContext(dir)
	assert.Empty(t, got, "expected empty string for no files")
}

func TestCrawlProjectContext_AgentsMD(t *testing.T) {
	dir := t.TempDir()
	content := "# Project Conventions\n\nAlways use tabs for indentation.\n"
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got := crawlProjectContext(dir)
	assert.Contains(t, got, "AGENTS.md", "expected AGENTS.md section")
	assert.Contains(t, got, "Always use tabs", "expected convention content")
}

func TestCrawlProjectContext_SystemMD(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".code-warden"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "# System\n\nRun tests before committing.\n"
	if err := os.WriteFile(filepath.Join(dir, ".code-warden", "SYSTEM.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got := crawlProjectContext(dir)
	assert.Contains(t, got, "System Instructions", "expected System Instructions section")
	assert.Contains(t, got, "Run tests before committing", "expected system content")
}

func TestCrawlProjectContext_Skills(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".code-warden", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	skillContent := "# TDD Workflow\n\nWrite tests first.\n"
	if err := os.WriteFile(filepath.Join(dir, ".code-warden", "skills", "tdd.md"), []byte(skillContent), 0o644); err != nil {
		t.Fatal(err)
	}

	got := crawlProjectContext(dir)
	assert.Contains(t, got, "Skill: tdd", "expected Skill: tdd section")
}

func TestCrawlProjectContext_BothFiles(t *testing.T) {
	dir := t.TempDir()

	agentsContent := "# Agents\n\nUse Go 1.22.\n"
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(agentsContent), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(dir, ".code-warden"), 0o755); err != nil {
		t.Fatal(err)
	}
	sysContent := "# System\n\nRun lint.\n"
	if err := os.WriteFile(filepath.Join(dir, ".code-warden", "SYSTEM.md"), []byte(sysContent), 0o644); err != nil {
		t.Fatal(err)
	}

	got := crawlProjectContext(dir)
	assert.Contains(t, got, "AGENTS.md", "expected AGENTS.md section")
	assert.Contains(t, got, "System Instructions", "expected System Instructions section")
}

func TestCrawlProjectContext_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	got := crawlProjectContext(dir)
	assert.Empty(t, got, "expected empty string for empty file")
}
