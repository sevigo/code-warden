package review

import (
	"testing"
)

func TestParseGapOutput_ValidJSON(t *testing.T) {
	input := `{"gaps":[{"tool":"search_code","reason":"find error handling","args":{"query":"error handling","limit":5}}]}`
	gaps, err := parseGapOutput(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gaps) != 1 {
		t.Fatalf("expected 1 gap, got %d", len(gaps))
	}
	if gaps[0].Tool != "search_code" {
		t.Errorf("expected tool=search_code, got %s", gaps[0].Tool)
	}
	if gaps[0].Reason != "find error handling" {
		t.Errorf("unexpected reason: %s", gaps[0].Reason)
	}
}

func TestParseGapOutput_EmptyGaps(t *testing.T) {
	input := `{"gaps":[]}`
	gaps, err := parseGapOutput(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gaps) != 0 {
		t.Errorf("expected 0 gaps, got %d", len(gaps))
	}
}

func TestParseGapOutput_StripMarkdownFences(t *testing.T) {
	input := "```json\n{\"gaps\":[]}\n```"
	gaps, err := parseGapOutput(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gaps) != 0 {
		t.Errorf("expected 0 gaps, got %d", len(gaps))
	}
}

func TestParseGapOutput_StripPlainFences(t *testing.T) {
	input := "```\n{\"gaps\":[{\"tool\":\"get_symbol\",\"reason\":\"need type\",\"args\":{\"name\":\"Foo\"}}]}\n```"
	gaps, err := parseGapOutput(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gaps) != 1 {
		t.Fatalf("expected 1 gap, got %d", len(gaps))
	}
	if gaps[0].Tool != "get_symbol" {
		t.Errorf("expected tool=get_symbol, got %s", gaps[0].Tool)
	}
}

func TestParseGapOutput_InvalidJSON(t *testing.T) {
	_, err := parseGapOutput("not json at all")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParseGapOutput_MultipleGaps(t *testing.T) {
	input := `{
		"gaps": [
			{"tool": "search_code", "reason": "r1", "args": {"query": "q1"}},
			{"tool": "get_symbol",  "reason": "r2", "args": {"name": "MyType"}},
			{"tool": "find_usages", "reason": "r3", "args": {"symbol": "Process"}}
		]
	}`
	gaps, err := parseGapOutput(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gaps) != 3 {
		t.Fatalf("expected 3 gaps, got %d", len(gaps))
	}
}

func TestParseLimitArg_Float64(t *testing.T) {
	if got := parseLimitArg(float64(7)); got != 7 {
		t.Errorf("expected 7, got %d", got)
	}
}

func TestParseLimitArg_Int(t *testing.T) {
	if got := parseLimitArg(3); got != 3 {
		t.Errorf("expected 3, got %d", got)
	}
}

func TestParseLimitArg_Missing(t *testing.T) {
	if got := parseLimitArg(nil); got != 5 {
		t.Errorf("expected default 5, got %d", got)
	}
}

func TestParseLimitArg_Negative(t *testing.T) {
	if got := parseLimitArg(float64(-1)); got != 5 {
		t.Errorf("expected default 5 for negative value, got %d", got)
	}
}

func TestTruncateStr_ShortString(t *testing.T) {
	if got := truncateStr("hello", 10); got != "hello" {
		t.Errorf("unexpected truncation: %q", got)
	}
}

func TestTruncateStr_ExactLength(t *testing.T) {
	if got := truncateStr("hello", 5); got != "hello" {
		t.Errorf("unexpected truncation: %q", got)
	}
}

func TestTruncateStr_Overflow(t *testing.T) {
	got := truncateStr("hello world", 5)
	if got != "hello\n...[truncated]" {
		t.Errorf("unexpected result: %q", got)
	}
}

func TestEscapeCodeFences_NoFences(t *testing.T) {
	input := "func main() { println(\"hello\") }"
	got := escapeCodeFences(input)
	if got != input {
		t.Errorf("unexpected modification: %q", got)
	}
}

func TestEscapeCodeFences_WithFences(t *testing.T) {
	input := "code:\n```go\nfunc main() {}\n```\nmore"
	expected := "code:\n` ` `go\nfunc main() {}\n` ` `\nmore"
	got := escapeCodeFences(input)
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

func TestEscapeCodeFences_MultipleFences(t *testing.T) {
	input := "```\ncode1\n```\n```\ncode2\n```"
	expected := "` ` `\ncode1\n` ` `\n` ` `\ncode2\n` ` `"
	got := escapeCodeFences(input)
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}
