package contextpkg

import (
	"context"
	"testing"
	"time"

	"github.com/sevigo/goframe/schema"

	internalgithub "github.com/sevigo/code-warden/internal/github"
)

type mockBuilder struct {
	callCount int
	result    *ContextResult
}

func (m *mockBuilder) BuildRelevantContextWithImpact(_ context.Context, _, _, _ string, _ []internalgithub.ChangedFile, _ string) *ContextResult {
	m.callCount++
	return m.result
}

func (m *mockBuilder) BuildRelevantContext(ctx context.Context, collectionName, embedderModelName, repoPath string, changedFiles []internalgithub.ChangedFile, prDescription string) (string, string) {
	r := m.BuildRelevantContextWithImpact(ctx, collectionName, embedderModelName, repoPath, changedFiles, prDescription)
	return r.FullContext, r.DefinitionsContext
}

func (m *mockBuilder) BuildContextForPrompt(_ []schema.Document) string { return "" }
func (m *mockBuilder) GenerateArchSummaries(_ context.Context, _, _, _ string, _ []string) error {
	return nil
}
func (m *mockBuilder) GenerateComparisonSummaries(_ context.Context, _ []string, _ string, _ []string) (map[string]map[string]string, error) {
	return nil, nil
}
func (m *mockBuilder) GenerateProjectContext(_ context.Context, _, _ string) (string, error) {
	return "", nil
}
func (m *mockBuilder) GeneratePackageSummaries(_ context.Context, _, _ string) error {
	return nil
}

func TestContextCacheHit(t *testing.T) {
	cache := NewContextCache(5*time.Minute, 10)
	inner := &mockBuilder{result: &ContextResult{FullContext: "ctx", DefinitionsContext: "defs", ImpactRadius: 3}}
	b := NewCachingBuilder(inner, cache)

	files := []internalgithub.ChangedFile{{Filename: "main.go", Patch: "+hello"}}
	r1 := b.BuildRelevantContextWithImpact(context.Background(), "col1", "embed", "/repo", files, "pr desc")
	r2 := b.BuildRelevantContextWithImpact(context.Background(), "col1", "embed", "/repo", files, "pr desc")

	if inner.callCount != 1 {
		t.Fatalf("expected 1 inner call, got %d", inner.callCount)
	}
	if r1.FullContext != r2.FullContext {
		t.Error("cached result should match original")
	}
}

func TestContextCacheMissDifferentFiles(t *testing.T) {
	cache := NewContextCache(5*time.Minute, 10)
	inner := &mockBuilder{result: &ContextResult{FullContext: "ctx"}}
	b := NewCachingBuilder(inner, cache)

	files1 := []internalgithub.ChangedFile{{Filename: "a.go", Patch: "+a"}}
	files2 := []internalgithub.ChangedFile{{Filename: "b.go", Patch: "+b"}}

	b.BuildRelevantContextWithImpact(context.Background(), "col1", "embed", "/repo", files1, "desc")
	b.BuildRelevantContextWithImpact(context.Background(), "col1", "embed", "/repo", files2, "desc")

	if inner.callCount != 2 {
		t.Fatalf("expected 2 inner calls for different files, got %d", inner.callCount)
	}
}

func TestContextCacheExpiration(t *testing.T) {
	cache := NewContextCache(1*time.Nanosecond, 10)
	inner := &mockBuilder{result: &ContextResult{FullContext: "ctx"}}
	b := NewCachingBuilder(inner, cache)

	files := []internalgithub.ChangedFile{{Filename: "main.go"}}
	b.BuildRelevantContextWithImpact(context.Background(), "col1", "embed", "/repo", files, "desc")
	time.Sleep(10 * time.Millisecond)
	b.BuildRelevantContextWithImpact(context.Background(), "col1", "embed", "/repo", files, "desc")

	if inner.callCount != 2 {
		t.Fatalf("expected 2 calls after expiration, got %d", inner.callCount)
	}
}
