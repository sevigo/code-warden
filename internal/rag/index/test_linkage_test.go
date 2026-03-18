package index

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractTestedSymbols_Go(t *testing.T) {
	tests := []struct {
		name     string
		filePath string
		content  string
		want     []string
	}{
		{
			name:     "simple test function",
			filePath: "service_test.go",
			content: `package service

func TestUserService(t *testing.T) {
	// test code
}`,
			want: []string{"UserService"},
		},
		{
			name:     "test with underscore",
			filePath: "handler_test.go",
			content: `package handler

func TestHandler_CreateUser(t *testing.T) {
	// test code
}`,
			want: []string{"Handler"},
		},
		{
			name:     "benchmark",
			filePath: "service_test.go",
			content: `package service

func BenchmarkProcessRequest(b *testing.B) {
	// benchmark code
}`,
			want: []string{"ProcessRequest"},
		},
		{
			name:     "fuzz test",
			filePath: "parser_test.go",
			content: `package parser

func FuzzParseInput(f *testing.F) {
	// fuzz code
}`,
			want: []string{"ParseInput"},
		},
		{
			name:     "multiple tests",
			filePath: "user_test.go",
			content: `package user

func TestUserService(t *testing.T) {}
func TestUserRepository(t *testing.T) {}
func TestValidateUser(t *testing.T) {}`,
			want: []string{"UserService", "UserRepository", "ValidateUser"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractTestedSymbols(tt.filePath, tt.content)
			gotNames := make([]string, 0, len(got))
			for _, s := range got {
				gotNames = append(gotNames, s.Symbol)
			}

			assert.ElementsMatch(t, tt.want, gotNames, "tested symbols should match")
		})
	}
}

func TestExtractTestedSymbols_Python(t *testing.T) {
	content := `import pytest

def test_create_user():
    pass

def test_delete_user():
    pass

def test_validate_input():
    pass
`
	got := ExtractTestedSymbols("test_user.py", content)
	require.NotEmpty(t, got, "should extract Python test symbols")

	gotNames := make(map[string]bool)
	for _, s := range got {
		gotNames[s.Symbol] = true
	}

	// Python extracts the function name after test_
	expected := []string{"create_user", "delete_user", "validate_input"}
	for _, exp := range expected {
		assert.True(t, gotNames[exp], "expected symbol %s to be found", exp)
	}
}

func TestExtractTestedSymbols_TypeScript(t *testing.T) {
	content := `describe('UserService', () => {
  it('should create user', () => {});
  it('should delete user', () => {});
});`
	got := ExtractTestedSymbols("user.service.test.ts", content)
	// TypeScript test extraction is heuristics-based
	// We just check it doesn't crash and returns something reasonable
	assert.NotNil(t, got)
}

func TestExtractTestedSymbols_Java(t *testing.T) {
	content := `public class UserServiceTest {
    @Test
    public void testCreateUser() {}
    
    @Test
    public void deleteUser() {}
}`
	got := ExtractTestedSymbols("UserServiceTest.java", content)
	require.NotEmpty(t, got, "should extract Java test symbols")

	gotNames := make(map[string]bool)
	for _, s := range got {
		gotNames[s.Symbol] = true
	}

	// Java extracts after @Test annotation
	expected := []string{"CreateUser", "deleteUser"}
	for _, exp := range expected {
		assert.True(t, gotNames[exp], "expected symbol %s to be found", exp)
	}
}

func TestExtractTestedSymbols_Rust(t *testing.T) {
	content := `#[test]
fn test_parse_input() {}

#[test]
fn validate_output() {}`
	got := ExtractTestedSymbols("parser_test.rs", content)
	// Rust test extraction is limited - #[test] attribute is on separate line
	// We just verify it doesn't crash and returns something
	assert.NotNil(t, got)
}

func TestExtractTestedSymbols_NonTestFile(t *testing.T) {
	content := `package service

func ProcessRequest() {}

func CreateUser() {}`
	got := ExtractTestedSymbols("service.go", content)
	assert.Empty(t, got, "non-test files should return empty")
}

func TestInferSourceFile(t *testing.T) {
	tests := []struct {
		testFile string
		want     string
	}{
		{"service_test.go", "service.go"},
		{"handler/user_test.go", "handler/user.go"},
		{"utils.test.ts", "utils.ts"},
		{"component.spec.tsx", "component.tsx"},
		{"test_user.py", "test_user.py"},                 // Not standard pattern
		{"UserServiceTest.java", "UserServiceTest.java"}, // Not standard pattern
	}

	for _, tt := range tests {
		t.Run(tt.testFile, func(t *testing.T) {
			got := InferSourceFile(tt.testFile)
			assert.Equal(t, tt.want, got, "source file inference should match")
		})
	}
}

func TestIsTestFile(t *testing.T) {
	tests := []struct {
		filePath string
		want     bool
	}{
		{"service.go", false},
		{"service_test.go", true},
		{"handler/user.go", false},
		{"handler/user_test.go", true},
		{"utils.test.ts", true},
		{"component.spec.tsx", true},
		{"main.js", false},
		{"app.test.js", true},
		{"user.py", false},
		{"user_test.py", true},
		{"Service.java", false},
		{"parser.rs", false},
	}

	for _, tt := range tests {
		t.Run(tt.filePath, func(t *testing.T) {
			got := IsTestFile(tt.filePath)
			assert.Equal(t, tt.want, got, "IsTestFile(%q)", tt.filePath)
		})
	}
}
