package agent

import (
	"fmt"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// normalizeForFuzzyMatch applies progressive transformations to make
// AI-generated text match file content despite minor formatting differences.
// Handles:
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

// bomUTF8 is the UTF-8 byte-order mark.
const bomUTF8 = "\xEF\xBB\xBF"

// stripBOM removes a leading UTF-8 BOM from content.
// Returns the stripped content and whether a BOM was found.
func stripBOM(content string) (string, bool) {
	if strings.HasPrefix(content, bomUTF8) {
		return content[len(bomUTF8):], true
	}
	return content, false
}

// prependBOM adds a UTF-8 BOM to content if hasBOM is true.
func prependBOM(content string, hasBOM bool) string {
	if hasBOM {
		return bomUTF8 + content
	}
	return content
}

// detectLineEnding returns the dominant line ending in content.
// Checks the first line only for efficiency (mirrors Pi's approach).
func detectLineEnding(content string) string {
	idx := strings.Index(content, "\n")
	if idx > 0 && content[idx-1] == '\r' {
		return "\r\n"
	}
	return "\n"
}

// normalizeLineEndings converts all CRLF sequences to LF.
func normalizeLineEndings(content string) string {
	return strings.ReplaceAll(content, "\r\n", "\n")
}

// restoreLineEndings converts LF to the detected line ending.
// Only performs replacement if lineEnding is CRLF.
func restoreLineEndings(content, lineEnding string) string {
	if lineEnding == "\r\n" {
		return strings.ReplaceAll(content, "\n", "\r\n")
	}
	return content
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
// matchResult holds the resolved position of a single edit in the working content.
// found must be true before index/matchLen are meaningful; a zero-value struct
// is intentionally an unresolved match to avoid confusion with a real match at
// byte offset 0.
type matchResult struct {
	found    bool
	index    int
	matchLen int
}

// applyMultiEdit applies multiple (old→new) replacements.
// If any pair requires fuzzy matching the entire content is normalised first,
// ensuring all replacements operate in a consistent character space.
// Replacements are applied in descending position order so earlier indices
// are not shifted by later writes.
//
// Rules (mirroring Pi's applyEditsToNormalizedContent):
//   - Each OldStr must match exactly once (error if 0 or >1 occurrences).
//   - No two matches may overlap (error if they do).
//
// If some edits match but others fail, the successful edits are still applied
// and the returned error lists the failed edit indices. The caller can decide
// whether to keep the partial result.
//
// WARNING: When fuzzy matching is used the returned content is the
// NFKC-normalised form of the entire file — characters outside the edited
// regions (smart quotes, ligatures, special spaces) are converted to their
// ASCII equivalents. Acceptable for code; may alter documentation.
func applyMultiEdit(content string, edits []editPair) (string, bool, error) {
	if len(edits) == 0 {
		return content, false, nil
	}

	// Phase 1: attempt exact matches; note whether any edit requires fuzzy.
	matches, needsFuzzy, err := exactMatches(content, edits)
	if err != nil {
		return "", false, err
	}

	// Phase 2: if any edit missed exactly, normalise the entire content and
	// re-resolve all matches in the normalised space.
	working := content
	if needsFuzzy {
		working = normalizeForFuzzyMatch(content)
		matches, err = fuzzyMatches(working, edits)
		if err != nil {
			return "", false, err
		}
	}

	// Phase 3: separate successful and failed matches.
	var succeeded []int
	var failedIndices []int
	for i, m := range matches {
		if m.found {
			succeeded = append(succeeded, i)
		} else {
			failedIndices = append(failedIndices, i)
		}
	}

	// Phase 4: validate no overlapping ranges among succeeded matches.
	succeededMatches := make([]matchResult, 0, len(succeeded))
	for _, i := range succeeded {
		succeededMatches = append(succeededMatches, matches[i])
	}
	if err := validateNoOverlaps(succeededMatches); err != nil {
		return "", false, err
	}

	// Phase 5: sort by position descending and apply in that order.
	order := descendingOrder(succeededMatches)
	out := working
	for _, idx := range order {
		// idx is an index into succeededMatches, which corresponds to
		// succeeded[idx] in the original edits slice. We need the original
		// edit index to look up the match and replacement.
		origIdx := succeeded[idx]
		m := matches[origIdx]
		out = out[:m.index] + edits[origIdx].NewStr + out[m.index+m.matchLen:]
	}

	if len(failedIndices) > 0 {
		return out, needsFuzzy, &partialEditError{
			FailedIndices: failedIndices,
			TotalEdits:    len(edits),
		}
	}

	return out, needsFuzzy, nil
}

// partialEditError is returned when some edits in a multi-edit batch
// succeeded but others failed. The caller can inspect FailedIndices to know
// which edits were not applied. The returned content contains all successful
// edits already applied.
type partialEditError struct {
	FailedIndices []int
	TotalEdits    int
}

func (e *partialEditError) Error() string {
	return fmt.Sprintf("edit_file: applied %d of %d edits; edits at indices %v not found",
		e.TotalEdits-len(e.FailedIndices), e.TotalEdits, e.FailedIndices)
}

// exactMatches tries an exact string count for each edit.
// Returns matches (zero-value for misses), whether any edit needs fuzzy, and
// any hard error (e.g. ambiguous match).
func exactMatches(content string, edits []editPair) ([]matchResult, bool, error) {
	matches := make([]matchResult, len(edits))
	needsFuzzy := false
	for i, e := range edits {
		switch cnt := strings.Count(content, e.OldStr); {
		case cnt == 1:
			matches[i] = matchResult{found: true, index: strings.Index(content, e.OldStr), matchLen: len(e.OldStr)}
		case cnt > 1:
			return nil, false, fmt.Errorf("edit_file: edits[%d] old_string appears %d times; provide more context to make it unique", i, cnt)
		default: // cnt == 0
			needsFuzzy = true
		}
	}
	return matches, needsFuzzy, nil
}

// fuzzyMatches re-resolves all edit positions in the already-normalised working
// content. Ambiguous matches (more than one occurrence) are hard errors.
// Missing matches return a zero-value matchResult (found=false) so the caller
// can apply partial edits.
func fuzzyMatches(working string, edits []editPair) ([]matchResult, error) {
	matches := make([]matchResult, len(edits))
	for i, e := range edits {
		normOld := normalizeForFuzzyMatch(e.OldStr)
		cnt := strings.Count(working, normOld)
		switch {
		case cnt == 0:
			// Not found — leave as zero-value (found=false). Caller decides
			// whether to proceed with partial edits.
		case cnt > 1:
			return nil, fmt.Errorf("edit_file: edits[%d] old_string appears %d times after normalization; provide more context to make it unique", i, cnt)
		default:
			matches[i] = matchResult{found: true, index: strings.Index(working, normOld), matchLen: len(normOld)}
		}
	}
	return matches, nil
}

// validateNoOverlaps returns an error if any two match ranges intersect.
func validateNoOverlaps(matches []matchResult) error {
	for i := range matches {
		iEnd := matches[i].index + matches[i].matchLen
		for j := i + 1; j < len(matches); j++ {
			jEnd := matches[j].index + matches[j].matchLen
			if matches[i].index < jEnd && matches[j].index < iEnd {
				return fmt.Errorf("edit_file: edits[%d] and edits[%d] overlap", i, j)
			}
		}
	}
	return nil
}

// descendingOrder returns indices sorted by match position descending so
// replacements can be applied without shifting earlier indices.
func descendingOrder(matches []matchResult) []int {
	order := make([]int, len(matches))
	for i := range order {
		order[i] = i
	}
	for i := 1; i < len(order); i++ {
		for j := i; j > 0 && matches[order[j]].index > matches[order[j-1]].index; j-- {
			order[j], order[j-1] = order[j-1], order[j]
		}
	}
	return order
}

// applyEdit performs a single text replacement using exact-then-fuzzy matching.
// It delegates to applyMultiEdit with a one-element slice.
//
// Note: when the edit is not found, applyMultiEdit returns a partialEditError
// (with FailedIndices=[0]). Callers that need to distinguish "not found" from
// other errors should check for *partialEditError via errors.As.
//
// WARNING: When fuzzy matching is used, the returned content is the
// NFKC-normalized form of the entire file. This means characters outside
// the edited region (smart quotes, ligatures, special spaces) will be
// converted to their ASCII equivalents. This is an acceptable trade-off
// for code files but may alter non-code content.
func applyEdit(content, oldText, newText string) (result string, usedFuzzy bool, err error) {
	return applyMultiEdit(content, []editPair{{OldStr: oldText, NewStr: newText}})
}
