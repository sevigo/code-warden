package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCrawlProjectContext_NoFiles(t *testing.T) {
	dir := t.TempDir()
	got := crawlProjectContext(dir)
	if got != "" {
		t.Errorf("expected empty string for no files, got: %q", got)
	}
}

func TestCrawlProjectContext_AgentsMD(t *testing.T) {
	dir := t.TempDir()
	content := "# Project Conventions\n\nAlways use tabs for indentation.\n"
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got := crawlProjectContext(dir)
	if !contains(got, "AGENTS.md") {
		t.Errorf("expected AGENTS.md section, got: %s", got)
	}
	if !contains(got, "Always use tabs") {
		t.Errorf("expected convention content, got: %s", got)
	}
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
	if !contains(got, "System Instructions") {
		t.Errorf("expected System Instructions section, got: %s", got)
	}
	if !contains(got, "Run tests before committing") {
		t.Errorf("expected system content, got: %s", got)
	}
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
	if !contains(got, "Skill: tdd") {
		t.Errorf("expected Skill: tdd section, got: %s", got)
	}
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
	if !contains(got, "AGENTS.md") {
		t.Error("expected AGENTS.md section")
	}
	if !contains(got, "System Instructions") {
		t.Error("expected System Instructions section")
	}
}

func TestCrawlProjectContext_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	got := crawlProjectContext(dir)
	if got != "" {
		t.Errorf("expected empty string for empty file, got: %q", got)
	}
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
