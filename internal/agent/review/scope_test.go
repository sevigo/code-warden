package review

import (
	"testing"

	"github.com/stretchr/testify/require"

	internalgithub "github.com/sevigo/code-warden/internal/github"
)

func TestScopeAnglesAllRelevant(t *testing.T) {
	changed := []internalgithub.ChangedFile{
		{Filename: "main.go", Patch: "+func main() { for _, r := range rows { query(r) } }"},
	}
	got := scopeAngles(DefaultAngles, changed)
	require.Len(t, got, 4)
}

func TestScopeAnglesDocOnly(t *testing.T) {
	changed := []internalgithub.ChangedFile{
		{Filename: "README.md", Patch: "+# Updated docs"},
	}
	got := scopeAngles(DefaultAngles, changed)
	// No code files → bug and conventions are dropped. Security and
	// performance are also dropped. Conservative fallback returns all angles
	// only when out is empty — here out is empty so all are kept.
	require.Len(t, got, 4)
}

func TestScopeAnglesSecurityOnly(t *testing.T) {
	changed := []internalgithub.ChangedFile{
		{Filename: "auth.go", Patch: "+func login(user, password string) { token := generateToken() }"},
	}
	got := scopeAngles(DefaultAngles, changed)
	// auth.go touches security keywords → security relevant.
	// It's a .go file → bug and conventions relevant.
	// No perf keywords → performance dropped.
	names := angleNames(got)
	require.Contains(t, names, "security")
	require.Contains(t, names, "bug")
	require.Contains(t, names, "conventions")
	require.NotContains(t, names, "performance")
}

func TestScopeAnglesPerfOnly(t *testing.T) {
	changed := []internalgithub.ChangedFile{
		{Filename: "cache.go", Patch: "+func (c *Cache) Get(key string) { c.mu.Lock(); defer c.mu.Unlock() }"},
	}
	got := scopeAngles(DefaultAngles, changed)
	// cache.go touches lock/mutex → performance relevant.
	// It's a .go file → bug and conventions relevant.
	// No security keywords → security dropped.
	names := angleNames(got)
	require.Contains(t, names, "performance")
	require.Contains(t, names, "bug")
	require.Contains(t, names, "conventions")
	require.NotContains(t, names, "security")
}

func TestScopeAnglesNoChangedFiles(t *testing.T) {
	got := scopeAngles(DefaultAngles, nil)
	require.Len(t, got, 4)
}

func angleNames(angles []Angle) []string {
	var names []string
	for _, a := range angles {
		names = append(names, a.Name)
	}
	return names
}
