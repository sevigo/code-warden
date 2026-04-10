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

// editPair is a single old→new replacement to be applied to a file.
type editPair struct {
	OldStr string
	NewStr string
}

// applyMultiEdit applies multiple (old→new) replacements atomically.
// If any pair requires fuzzy matching the entire content is normalised first,
// ensuring all replacements operate in a consistent character space.
// Replacements are applied in descending position order so earlier indices
// are not shifted by later writes.
//
// Rules (mirroring Pi's applyEditsToNormalizedContent):
//   - Each OldStr must match exactly once (error if 0 or >1 occurrences).
//   - No two matches may overlap (error if they do).
//
// WARNING: When fuzzy matching is used the returned content is the
// NFKC-normalised form of the entire file — characters outside the edited
// regions (smart quotes, ligatures, special spaces) are converted to their
// ASCII equivalents. Acceptable for code; may alter documentation.
func applyMultiEdit(content string, edits []editPair) (result string, usedFuzzy bool, err error) {
	if len(edits) == 0 {
		return content, false, nil
	}

	// Phase 1: attempt exact matches for all edits, note if any need fuzzy.
	type matchResult struct {
		index    int
		matchLen int
	}
	matches := make([]matchResult, len(edits))
	needsFuzzy := false

	for i, e := range edits {
		cnt := strings.Count(content, e.OldStr)
		if cnt == 1 {
			matches[i] = matchResult{strings.Index(content, e.OldStr), len(e.OldStr)}
		} else if cnt > 1 {
			return "", false, fmt.Errorf("edit_file: edits[%d] old_string appears %d times; provide more context to make it unique", i, cnt)
		} else {
			needsFuzzy = true
		}
	}

	// Phase 2: if any edit missed exactly, normalise the entire content and
	// re-resolve all matches in the normalised space.
	working := content
	if needsFuzzy {
		working = normalizeForFuzzyMatch(content)
		usedFuzzy = true
		for i, e := range edits {
			if matches[i].index != 0 || matches[i].matchLen != 0 {
				// Re-find in normalised space (was exact in original space but
				// the index is now invalid; normalisation may change byte offsets).
				matches[i] = matchResult{}
			}
			normOld := normalizeForFuzzyMatch(e.OldStr)
			cnt := strings.Count(working, normOld)
			if cnt == 0 {
				return "", false, fmt.Errorf("edit_file: edits[%d] old_string not found (tried exact match and fuzzy normalization)", i)
			}
			if cnt > 1 {
				return "", false, fmt.Errorf("edit_file: edits[%d] old_string appears %d times after normalization; provide more context to make it unique", i, cnt)
			}
			matches[i] = matchResult{strings.Index(working, normOld), len(normOld)}
		}
	} else {
		// All exact — re-index in original content (already done above).
	}

	// Phase 3: validate no overlapping ranges.
	for i := range matches {
		iEnd := matches[i].index + matches[i].matchLen
		for j := i + 1; j < len(matches); j++ {
			jEnd := matches[j].index + matches[j].matchLen
			if matches[i].index < jEnd && matches[j].index < iEnd {
				return "", false, fmt.Errorf("edit_file: edits[%d] and edits[%d] overlap", i, j)
			}
		}
	}

	// Phase 4: sort by position descending, apply in that order.
	// Use a simple insertion sort — edit count is always small.
	order := make([]int, len(edits))
	for i := range order {
		order[i] = i
	}
	for i := 1; i < len(order); i++ {
		for j := i; j > 0 && matches[order[j]].index > matches[order[j-1]].index; j-- {
			order[j], order[j-1] = order[j-1], order[j]
		}
	}

	out := working
	newStrFor := func(i int) string {
		if needsFuzzy {
			return edits[i].NewStr // newStr is always applied verbatim
		}
		return edits[i].NewStr
	}
	for _, i := range order {
		m := matches[i]
		out = out[:m.index] + newStrFor(i) + out[m.index+m.matchLen:]
	}

	return out, usedFuzzy, nil
}

// applyEdit performs a single text replacement using exact-then-fuzzy matching.
// It delegates to applyMultiEdit with a one-element slice.
//
// WARNING: When fuzzy matching is used, the returned content is the
// NFKC-normalized form of the entire file. This means characters outside
// the edited region (smart quotes, ligatures, special spaces) will be
// converted to their ASCII equivalents. This is an acceptable trade-off
// for code files but may alter non-code content.
func applyEdit(content, oldText, newText string) (result string, usedFuzzy bool, err error) {
	return applyMultiEdit(content, []editPair{{OldStr: oldText, NewStr: newText}})
}
