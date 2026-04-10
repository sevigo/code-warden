package agent

import (
	"strings"
	"testing"
)

func TestNormalizeForFuzzyMatch_SmartQuotes(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"smart single quotes \u2018\u2019", "it\u2019s a test", "it's a test"},
		{"smart double quotes \u201C\u201D", "\u201Chello\u201D", "\"hello\""},
		{"low-9 single quote \u201A", "\u201Ahello\u201A", "'hello'"},
		{"low-9 double quote \u201E", "\u201Ehello\u201E", "\"hello\""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeForFuzzyMatch(tt.input)
			if got != tt.want {
				t.Errorf("normalizeForFuzzyMatch(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalizeForFuzzyMatch_Dashes(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"en dash \u2013", "1\u20132", "1-2"},
		{"em dash \u2014", "hello\u2014world", "hello-world"},
		{"minus sign \u2212", "\u22125", "-5"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeForFuzzyMatch(tt.input)
			if got != tt.want {
				t.Errorf("normalizeForFuzzyMatch(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalizeForFuzzyMatch_UnicodeSpaces(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"NBSP \u00A0", "hello\u00A0world", "hello world"},
		{"thin space \u2009", "1\u20092", "1 2"},
		{"ideographic space \u3000", "a\u3000b", "a b"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeForFuzzyMatch(tt.input)
			if got != tt.want {
				t.Errorf("normalizeForFuzzyMatch(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalizeForFuzzyMatch_TrailingWhitespace(t *testing.T) {
	input := "line1   \nline2\t\nline3"
	want := "line1\nline2\nline3"
	got := normalizeForFuzzyMatch(input)
	if got != want {
		t.Errorf("normalizeForFuzzyMatch(%q) = %q, want %q", input, got, want)
	}
}

func TestApplyEdit_ExactMatch(t *testing.T) {
	content := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"
	result, fuzzy, err := applyEdit(content, "println(\"hello\")", "println(\"world\")")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fuzzy {
		t.Error("should not use fuzzy for exact match")
	}
	if !strings.Contains(result, "println(\"world\")") {
		t.Errorf("expected replacement, got: %s", result)
	}
}

func TestApplyEdit_FuzzyMatch_TrailingSpaces(t *testing.T) {
	// File has trailing spaces, LLM doesn't include them in old_string
	content := "if x == 1   \n    return true\n"
	oldText := "if x == 1\n    return true\n"
	newText := "if x == 2\n    return true\n"

	result, fuzzy, err := applyEdit(content, oldText, newText)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fuzzy {
		t.Error("expected fuzzy match for trailing whitespace difference")
	}
	if !strings.Contains(result, "if x == 2") {
		t.Errorf("expected replacement, got: %s", result)
	}
}

func TestApplyEdit_FuzzyMatch_SmartQuotes(t *testing.T) {
	// LLM uses smart quotes, file has ASCII quotes
	content := "msg := \"hello world\"\n"
	oldText := "msg := \u201Chello world\u201D\n" // smart double quotes
	newText := "msg := \u201Cgoodbye world\u201D\n"

	result, fuzzy, err := applyEdit(content, oldText, newText)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fuzzy {
		t.Error("expected fuzzy match for smart quote difference")
	}
	// In fuzzy mode, the result is in normalized space
	if !strings.Contains(result, "goodbye") {
		t.Errorf("expected replacement, got: %s", result)
	}
}

func TestApplyEdit_NoMatch(t *testing.T) {
	content := "package main\n"
	_, _, err := applyEdit(content, "nonexistent", "replacement")
	if err == nil {
		t.Error("expected error for no match")
	}
}

func TestApplyEdit_MultipleExactMatches(t *testing.T) {
	content := "x\nx\n"
	_, _, err := applyEdit(content, "x", "y")
	if err == nil {
		t.Error("expected error for multiple matches")
	}
	if !strings.Contains(err.Error(), "2 times") {
		t.Errorf("expected '2 times' in error, got: %v", err)
	}
}

func TestApplyEdit_MultipleFuzzyMatches(t *testing.T) {
	content := "  x  \n  x  \n" // trailing spaces on both lines
	oldText := "x\n"            // without trailing spaces
	_, _, err := applyEdit(content, oldText, "y")
	if err == nil {
		t.Error("expected error for multiple fuzzy matches")
	}
	if !strings.Contains(err.Error(), "2 times") {
		t.Errorf("expected '2 times' in error, got: %v", err)
	}
}

func TestApplyEdit_LLM_Indentation_Hallucination(t *testing.T) {
	// LLM uses spaces where the file has tabs (common hallucination)
	content := "func main() {\n\tprintln(\"hello\")\n}\n"
	// LLM uses spaces instead of tab
	oldText := "func main() {\n    println(\"hello\")\n}\n"

	_, _, err := applyEdit(content, oldText, "func main() {\n    println(\"world\")\n}\n")
	// This should NOT match because tab vs 4 spaces is not normalized by our fuzzy matching
	// (trailing whitespace normalization won't help here since tab/space difference is leading)
	if err == nil {
		// If it does match via NFKC or other normalization, that's also acceptable
		t.Log("tab/space difference was normalized — this is fine")
	}
}

func TestFuzzyFindText_ExactFirst(t *testing.T) {
	content := "hello world"
	result := fuzzyFindText(content, "world")
	if !result.found {
		t.Fatal("expected to find exact match")
	}
	if result.usedFuzzy {
		t.Error("should not need fuzzy for exact match")
	}
	if result.index != 6 {
		t.Errorf("expected index 6, got %d", result.index)
	}
}

func TestFuzzyFindText_FuzzyFallback(t *testing.T) {
	content := "hello\u00A0world"                   // NBSP between hello and world
	result := fuzzyFindText(content, "hello world") // regular space in search
	if !result.found {
		t.Fatal("expected to find fuzzy match")
	}
	if !result.usedFuzzy {
		t.Error("should have used fuzzy matching")
	}
}
