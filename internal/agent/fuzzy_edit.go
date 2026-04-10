package agent

import (
	"fmt"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// normalizeForFuzzyMatch applies progressive transformations to make
// LLM-generated text match file content despite minor formatting differences.
// Ported from Pi's edit-diff.ts to handle:
//   - Unicode NFKC normalization (smart quotes, ligatures, etc.)
//   - Trailing whitespace per line
//   - Smart single/double quotes → ASCII
//   - Unicode dashes/hyphens → ASCII hyphen
//   - Unicode spaces (NBSP, etc.) → regular space
func normalizeForFuzzyMatch(text string) string {
	// NFKC normalization folds smart quotes, ligatures, etc.
	result := norm.NFKC.String(text)

	// Strip trailing whitespace per line
	result = stripTrailingWhitespace(result)

	// Smart single quotes → '
	result = strings.NewReplacer(
		"\u2018", "'",
		"\u2019", "'",
		"\u201A", "'",
		"\u201B", "'",
	).Replace(result)

	// Smart double quotes → "
	result = strings.NewReplacer(
		"\u201C", "\"",
		"\u201D", "\"",
		"\u201E", "\"",
		"\u201F", "\"",
	).Replace(result)

	// Various dashes/hyphens → -
	result = strings.NewReplacer(
		"\u2010", "-",
		"\u2011", "-",
		"\u2012", "-",
		"\u2013", "-",
		"\u2014", "-",
		"\u2015", "-",
		"\u2212", "-",
	).Replace(result)

	// Special spaces → regular space
	specialSpaceReplacer := strings.NewReplacer(
		"\u00A0", " ", // NO-BREAK SPACE
		"\u2002", " ", // EN SPACE
		"\u2003", " ", // EM SPACE
		"\u2004", " ", // THREE-PER-EM SPACE
		"\u2005", " ", // FOUR-PER-EM SPACE
		"\u2006", " ", // SIX-PER-EM SPACE
		"\u2007", " ", // FIGURE SPACE
		"\u2008", " ", // PUNCTUATION SPACE
		"\u2009", " ", // THIN SPACE
		"\u200A", " ", // HAIR SPACE
		"\u202F", " ", // NARROW NO-BREAK SPACE
		"\u205F", " ", // MEDIUM MATHEMATICAL SPACE
		"\u3000", " ", // IDEOGRAPHIC SPACE
	)
	result = specialSpaceReplacer.Replace(result)

	return result
}

func stripTrailingWhitespace(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t\r")
	}
	return strings.Join(lines, "\n")
}

// fuzzyFindResult holds the result of a fuzzy text search.
type fuzzyFindResult struct {
	found                 bool
	index                 int
	matchLen              int
	usedFuzzy             bool
	contentForReplacement string
}

// fuzzyFindText searches for oldText in content, trying exact match first,
// then falling back to fuzzy normalization matching.
func fuzzyFindText(content, oldText string) fuzzyFindResult {
	if idx := strings.Index(content, oldText); idx != -1 {
		return fuzzyFindResult{
			found:                 true,
			index:                 idx,
			matchLen:              len(oldText),
			usedFuzzy:             false,
			contentForReplacement: content,
		}
	}

	fuzzyContent := normalizeForFuzzyMatch(content)
	fuzzyOldText := normalizeForFuzzyMatch(oldText)

	if idx := strings.Index(fuzzyContent, fuzzyOldText); idx != -1 {
		return fuzzyFindResult{
			found:                 true,
			index:                 idx,
			matchLen:              len(fuzzyOldText),
			usedFuzzy:             true,
			contentForReplacement: fuzzyContent,
		}
	}

	return fuzzyFindResult{
		found:                 false,
		index:                 -1,
		contentForReplacement: content,
	}
}

// applyEdit performs a text replacement using exact-then-fuzzy matching.
// It first tries an exact match; if that fails (not found or ambiguous),
// it falls back to fuzzy matching with Unicode normalization.
//
// WARNING: When fuzzy matching is used, the returned content is the
// NFKC-normalized form of the entire file. This means characters outside
// the edited region (smart quotes, ligatures, special spaces) will be
// converted to their ASCII equivalents. This is an acceptable trade-off
// for code files but may alter non-code content.
func applyEdit(content, oldText, newText string) (result string, usedFuzzy bool, err error) {
	count := strings.Count(content, oldText)
	if count == 1 {
		return strings.Replace(content, oldText, newText, 1), false, nil
	}

	if count > 1 {
		return "", false, fmt.Errorf("edit_file: old_string appears %d times; provide more context to make it unique", count)
	}

	fuzzyResult := fuzzyFindText(content, oldText)
	if !fuzzyResult.found {
		return "", false, fmt.Errorf("edit_file: old_string not found in file (tried exact match and fuzzy normalization)")
	}

	fuzzyContent := fuzzyResult.contentForReplacement
	fuzzyOldText := normalizeForFuzzyMatch(oldText)
	fuzzyCount := strings.Count(fuzzyContent, fuzzyOldText)
	if fuzzyCount > 1 {
		return "", false, fmt.Errorf("edit_file: old_string appears %d times after normalization; provide more context to make it unique", fuzzyCount)
	}

	newContent := fuzzyContent[:fuzzyResult.index] + newText + fuzzyContent[fuzzyResult.index+fuzzyResult.matchLen:]
	return newContent, true, nil
}
