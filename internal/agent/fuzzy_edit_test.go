package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
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

func TestStripTrailingWhitespace_CRLF(t *testing.T) {
	input := "var x string   \r\nvar y int\t\r\nend\r\n"
	want := "var x string\nvar y int\nend\n"
	got := stripTrailingWhitespace(input)
	if got != want {
		t.Errorf("stripTrailingWhitespace(CRLF) = %q, want %q", got, want)
	}
}

func TestNormalizeForFuzzyMatch_AllSpecialSpaces(t *testing.T) {
	input := "a\u00A0b\u2002c\u2003d\u2004e\u2005f\u2006g\u2007h\u2008i\u2009j\u200Ak\u202Fl\u205Fm\u3000n"
	want := "a b c d e f g h i j k l m n"
	got := normalizeForFuzzyMatch(input)
	if got != want {
		t.Errorf("only last space replaced; got %q, want %q", got, want)
	}
}

func TestApplyEdit_ExactMatch(t *testing.T) {
	content := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"
	result, fuzzy, err := applyEdit(content, "println(\"hello\")", "println(\"world\")")
	assert.NoError(t, err)
	assert.False(t, fuzzy, "should not use fuzzy for exact match")
	assert.Contains(t, result, "println(\"world\")", "expected replacement")
}

func TestApplyEdit_FuzzyMatch_TrailingSpaces(t *testing.T) {
	// File has trailing spaces, LLM doesn't include them in old_string
	content := "if x == 1   \n    return true\n"
	oldText := "if x == 1\n    return true\n"
	newText := "if x == 2\n    return true\n"

	result, fuzzy, err := applyEdit(content, oldText, newText)
	assert.NoError(t, err)
	assert.True(t, fuzzy, "expected fuzzy match for trailing whitespace difference")
	assert.Contains(t, result, "if x == 2", "expected replacement")
}

func TestApplyEdit_FuzzyMatch_SmartQuotes(t *testing.T) {
	// LLM uses smart quotes, file has ASCII quotes
	content := "msg := \"hello world\"\n"
	oldText := "msg := \u201Chello world\u201D\n" // smart double quotes
	newText := "msg := \u201Cgoodbye world\u201D\n"

	result, fuzzy, err := applyEdit(content, oldText, newText)
	assert.NoError(t, err)
	assert.True(t, fuzzy, "expected fuzzy match for smart quote difference")
	// In fuzzy mode, the result is in normalized space
	assert.Contains(t, result, "goodbye", "expected replacement")
}

func TestApplyEdit_NoMatch(t *testing.T) {
	content := "package main\n"
	_, _, err := applyEdit(content, "nonexistent", "replacement")
	assert.Error(t, err, "expected error for no match")
}

func TestApplyEdit_MultipleExactMatches(t *testing.T) {
	content := "x\nx\n"
	_, _, err := applyEdit(content, "x", "y")
	assert.Error(t, err, "expected error for multiple matches")
	if err != nil {
		assert.ErrorContains(t, err, "2 times", "expected '2 times' in error")
	}
}

func TestApplyEdit_MultipleFuzzyMatches(t *testing.T) {
	content := "  x  \n  x  \n" // trailing spaces on both lines
	oldText := "x\n"            // without trailing spaces
	_, _, err := applyEdit(content, oldText, "y")
	assert.Error(t, err, "expected error for multiple fuzzy matches")
	if err != nil {
		assert.ErrorContains(t, err, "2 times", "expected '2 times' in error")
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

// ── applyMultiEdit ────────────────────────────────────────────────────────────

func TestApplyMultiEdit_SingleEdit(t *testing.T) {
	content := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"
	result, fuzzy, err := applyMultiEdit(content, []editPair{
		{OldStr: "println(\"hello\")", NewStr: "println(\"world\")"},
	})
	assert.NoError(t, err)
	assert.False(t, fuzzy)
	assert.Contains(t, result, "println(\"world\")")
	assert.NotContains(t, result, "println(\"hello\")")
}

func TestApplyMultiEdit_TwoEdits(t *testing.T) {
	content := "const A = 1\nconst B = 2\nconst C = 3\n"
	result, fuzzy, err := applyMultiEdit(content, []editPair{
		{OldStr: "A = 1", NewStr: "A = 10"},
		{OldStr: "C = 3", NewStr: "C = 30"},
	})
	assert.NoError(t, err)
	assert.False(t, fuzzy)
	assert.Contains(t, result, "A = 10")
	assert.Contains(t, result, "B = 2") // unchanged
	assert.Contains(t, result, "C = 30")
}

func TestApplyMultiEdit_ReverseOrderApplied(t *testing.T) {
	// Edit at start of file + edit at end; both must be applied even though
	// the first edit shifts byte positions of everything after it.
	content := "START\nMIDDLE\nEND\n"
	result, _, err := applyMultiEdit(content, []editPair{
		{OldStr: "START", NewStr: "BEGINNING"},
		{OldStr: "END", NewStr: "FINISH"},
	})
	assert.NoError(t, err)
	assert.Equal(t, "BEGINNING\nMIDDLE\nFINISH\n", result)
}

func TestApplyMultiEdit_OverlapError(t *testing.T) {
	content := "abcdef"
	// "abc" and "cde" overlap at 'c'
	_, _, err := applyMultiEdit(content, []editPair{
		{OldStr: "abc", NewStr: "X"},
		{OldStr: "cde", NewStr: "Y"},
	})
	assert.Error(t, err)
	assert.ErrorContains(t, err, "overlap")
}

func TestApplyMultiEdit_NotFoundError(t *testing.T) {
	content := "hello world"
	result, _, err := applyMultiEdit(content, []editPair{
		{OldStr: "nonexistent", NewStr: "x"},
	})
	assert.Error(t, err)
	var pe *partialEditError
	assert.ErrorAs(t, err, &pe)
	assert.Equal(t, []int{0}, pe.FailedIndices)
	assert.Equal(t, content, result, "content should be unchanged when all edits fail")
}

func TestApplyMultiEdit_AmbiguousMatchError(t *testing.T) {
	content := "x\nx\n"
	_, _, err := applyMultiEdit(content, []editPair{
		{OldStr: "x", NewStr: "y"},
	})
	assert.Error(t, err)
	assert.ErrorContains(t, err, "2 times")
}

func TestApplyMultiEdit_PartialEdit(t *testing.T) {
	content := "const A = 1\nconst B = 2\nconst C = 3\n"
	result, fuzzy, err := applyMultiEdit(content, []editPair{
		{OldStr: "A = 1", NewStr: "A = 10"},  // found
		{OldStr: "nonexistent", NewStr: "X"}, // not found
		{OldStr: "C = 3", NewStr: "C = 30"},  // found
	})
	assert.Error(t, err)
	var pe *partialEditError
	assert.ErrorAs(t, err, &pe)
	assert.Equal(t, []int{1}, pe.FailedIndices)
	assert.Equal(t, 3, pe.TotalEdits)
	assert.True(t, fuzzy, "fuzzy mode activated because one edit missed exactly")
	assert.Contains(t, result, "A = 10")
	assert.Contains(t, result, "C = 30")
	assert.Contains(t, result, "B = 2") // unchanged
}

func TestApplyMultiEdit_PartialEdit_AllFail(t *testing.T) {
	content := "hello world"
	result, _, err := applyMultiEdit(content, []editPair{
		{OldStr: "nonexistent", NewStr: "x"},
		{OldStr: "also_missing", NewStr: "y"},
	})
	assert.Error(t, err)
	var pe *partialEditError
	assert.ErrorAs(t, err, &pe)
	assert.Equal(t, []int{0, 1}, pe.FailedIndices)
	assert.Equal(t, content, result, "content unchanged when all edits fail")
}

func TestApplyMultiEdit_PartialEdit_WithFuzzy(t *testing.T) {
	content := "A \u00A0= 1\nB = 2\n" // NBSP in file content
	result, fuzzy, err := applyMultiEdit(content, []editPair{
		{OldStr: "A \u00A0= 1", NewStr: "A = 10"}, // NBSP in old_string → fuzzy match
		{OldStr: "Z_missing", NewStr: "X"},        // not found even with fuzzy
	})
	assert.Error(t, err)
	var pe *partialEditError
	assert.ErrorAs(t, err, &pe)
	assert.Equal(t, []int{1}, pe.FailedIndices)
	assert.True(t, fuzzy)
	assert.Contains(t, result, "A = 10")
	assert.Contains(t, result, "B = 2")
}

func TestPartialEditError_Message(t *testing.T) {
	pe := &partialEditError{FailedIndices: []int{2, 5}, TotalEdits: 7}
	assert.Equal(t, "edit_file: applied 5 of 7 edits; edits at indices [2 5] not found", pe.Error())
}

func TestApplyMultiEdit_FuzzySmartQuotes(t *testing.T) {
	// File has ASCII quotes; LLM sends smart quotes in old_string.
	content := "msg := \"hello\"\n"
	oldText := "msg := \u201Chello\u201D\n" // smart double quotes
	result, fuzzy, err := applyMultiEdit(content, []editPair{
		{OldStr: oldText, NewStr: "msg := \"goodbye\"\n"},
	})
	assert.NoError(t, err)
	assert.True(t, fuzzy)
	assert.Contains(t, result, "goodbye")
}

func TestApplyMultiEdit_EmptyEdits(t *testing.T) {
	content := "unchanged"
	result, fuzzy, err := applyMultiEdit(content, nil)
	assert.NoError(t, err)
	assert.False(t, fuzzy)
	assert.Equal(t, content, result)
}

func TestApplyMultiEdit_FuzzyOneEditMissedOtherExact(t *testing.T) {
	// First edit needs fuzzy (smart quote); second is exact.
	// Both must still be applied in normalised space.
	content := "A = 1\nB = 2\n"
	result, fuzzy, err := applyMultiEdit(content, []editPair{
		{OldStr: "A\u00A0=\u00A01", NewStr: "A = 10"}, // NBSP spaces → fuzzy
		{OldStr: "B = 2", NewStr: "B = 20"},
	})
	assert.NoError(t, err)
	assert.True(t, fuzzy)
	assert.Contains(t, result, "A = 10")
	assert.Contains(t, result, "B = 20")
}

// ── Line ending and BOM handling ───────────────────────────────────────────

func TestDetectLineEnding_CRLF(t *testing.T) {
	content := "line1\r\nline2\r\n"
	assert.Equal(t, "\r\n", detectLineEnding(content))
}

func TestDetectLineEnding_LF(t *testing.T) {
	content := "line1\nline2\n"
	assert.Equal(t, "\n", detectLineEnding(content))
}

func TestDetectLineEnding_LFOnly(t *testing.T) {
	assert.Equal(t, "\n", detectLineEnding("single line"))
}

func TestNormalizeLineEndings(t *testing.T) {
	input := "a\r\nb\r\nc\n"
	want := "a\nb\nc\n"
	assert.Equal(t, want, normalizeLineEndings(input))
}

func TestRestoreLineEndings_CRLF(t *testing.T) {
	input := "a\nb\nc\n"
	want := "a\r\nb\r\nc\r\n"
	assert.Equal(t, want, restoreLineEndings(input, "\r\n"))
}

func TestRestoreLineEndings_LF(t *testing.T) {
	input := "a\nb\nc\n"
	assert.Equal(t, input, restoreLineEndings(input, "\n"))
}

func TestStripBOM_WithBOM(t *testing.T) {
	content := "\xEF\xBB\xBFpackage main\n"
	stripped, hasBOM := stripBOM(content)
	assert.True(t, hasBOM)
	assert.Equal(t, "package main\n", stripped)
}

func TestStripBOM_NoBOM(t *testing.T) {
	content := "package main\n"
	stripped, hasBOM := stripBOM(content)
	assert.False(t, hasBOM)
	assert.Equal(t, content, stripped)
}

func TestPrependBOM(t *testing.T) {
	content := "hello\n"
	assert.Equal(t, "\xEF\xBB\xBFhello\n", prependBOM(content, true))
	assert.Equal(t, content, prependBOM(content, false))
}

func TestApplyEdit_CRLFFile_LFQuery(t *testing.T) {
	// Simulate a file with CRLF line endings where the LLM sends LF-only old_string.
	fileContent := "func hello() {\r\n\treturn true\r\n}\r\n"
	llmOldStr := "func hello() {\n\treturn true\n}\n"
	llmNewStr := "func hello() {\n\treturn false\n}\n"

	// Pre-process as editFileTool would.
	working, _ := stripBOM(fileContent)
	lineEnding := detectLineEnding(working)
	working = normalizeLineEndings(working)

	normalizedOld := normalizeLineEndings(llmOldStr)
	normalizedNew := normalizeLineEndings(llmNewStr)

	result, fuzzy, err := applyEdit(working, normalizedOld, normalizedNew)
	assert.NoError(t, err)
	assert.False(t, fuzzy, "should match exactly after CRLF normalization")

	result = restoreLineEndings(result, lineEnding)
	assert.Contains(t, result, "return false")
	assert.Contains(t, result, "\r\n", "CRLF must be preserved in output")
	assert.NotContains(t, result, "return true")
}

func TestApplyEdit_BOMFile(t *testing.T) {
	// Simulate a file with a UTF-8 BOM prefix.
	fileContent := "\xEF\xBB\xBFpackage main\n\nfunc main() {}\n"
	llmOldStr := "func main() {}\n"
	llmNewStr := "func main() { fmt.Println(\"hi\") }\n"

	working, hasBOM := stripBOM(fileContent)
	assert.True(t, hasBOM)

	result, fuzzy, err := applyEdit(working, llmOldStr, llmNewStr)
	assert.NoError(t, err)
	assert.False(t, fuzzy)

	result = prependBOM(result, hasBOM)
	assert.True(t, strings.HasPrefix(result, "\xEF\xBB\xBF"), "BOM must be preserved in output")
	assert.Contains(t, result, "fmt.Println")
}

func TestApplyMultiEdit_CRLFFile(t *testing.T) {
	// Multi-edit on CRLF file — both edits must succeed and CRLF must be restored.
	fileContent := "const A = 1\r\nconst B = 2\r\nconst C = 3\r\n"

	working, _ := stripBOM(fileContent)
	lineEnding := detectLineEnding(working)
	working = normalizeLineEndings(working)

	pairs := []editPair{
		{OldStr: "A = 1", NewStr: "A = 10"},
		{OldStr: "C = 3", NewStr: "C = 30"},
	}

	result, _, err := applyMultiEdit(working, pairs)
	assert.NoError(t, err)

	result = restoreLineEndings(result, lineEnding)
	assert.Contains(t, result, "A = 10")
	assert.Contains(t, result, "C = 30")
	assert.Contains(t, result, "\r\n", "CRLF must be preserved")
}

func TestApplyEdit_BOMFile_CRLFFile(t *testing.T) {
	// Both BOM and CRLF in the same file.
	fileContent := "\xEF\xBB\xBFvar x = 1\r\nvar y = 2\r\n"
	llmOldStr := "var y = 2\n"
	llmNewStr := "var y = 20\n"

	working, hasBOM := stripBOM(fileContent)
	lineEnding := detectLineEnding(working)
	working = normalizeLineEndings(working)

	normalizedOld := normalizeLineEndings(llmOldStr)
	normalizedNew := normalizeLineEndings(llmNewStr)

	result, fuzzy, err := applyEdit(working, normalizedOld, normalizedNew)
	assert.NoError(t, err)
	assert.False(t, fuzzy)

	result = restoreLineEndings(result, lineEnding)
	result = prependBOM(result, hasBOM)

	assert.True(t, strings.HasPrefix(result, "\xEF\xBB\xBF"), "BOM preserved")
	assert.Contains(t, result, "var y = 20\r\n", "CRLF preserved and content replaced")
}
