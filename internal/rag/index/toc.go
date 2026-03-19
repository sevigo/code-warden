package index

import (
	"fmt"
	"strings"

	"github.com/sevigo/goframe/schema"
)

// isLikelyBoilerplate reports whether a chunk contains no meaningful code —
// only package declarations, import blocks, blank lines, and comment lines.
// Such chunks waste vector-store space and dilute search results, so they are
// skipped during indexing.
func isLikelyBoilerplate(content string) bool {
	for _, line := range strings.Split(strings.TrimSpace(content), "\n") {
		if !isBoilerplateLine(strings.TrimSpace(line)) {
			return false
		}
	}
	return true
}

// isBoilerplateLine returns true when a trimmed line carries no meaningful code.
func isBoilerplateLine(t string) bool {
	if t == "" || t == ")" || t == "import (" {
		return true
	}
	if strings.HasPrefix(t, "package ") || strings.HasPrefix(t, "import ") {
		return true
	}
	// Quoted/backtick import path (with or without alias)
	if strings.Contains(t, ` "`) || strings.Contains(t, " `") {
		return true
	}
	if (strings.HasPrefix(t, `"`) || strings.HasPrefix(t, "`")) &&
		(strings.HasSuffix(t, `"`) || strings.HasSuffix(t, "`")) {
		return true
	}
	// Comment prefixes used across all supported languages
	return strings.HasPrefix(t, "//") || strings.HasPrefix(t, "/*") ||
		strings.HasPrefix(t, "*") || strings.HasPrefix(t, "#")
}

// buildTOCChunk creates a single "table of contents" document for a source file.
// The chunk lists every exported symbol with its kind and signature, giving the
// LLM a fast orientation about what a file provides without having to retrieve
// individual function/type chunks.
//
// Returns nil when defDocs is empty (no exported symbols to list).
func buildTOCChunk(relPath string, defDocs []schema.Document) *schema.Document {
	if len(defDocs) == 0 {
		return nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "File: %s\n\n", relPath)
	sb.WriteString("Exported symbols:\n")

	pkgName := ""
	for _, def := range defDocs {
		identifier, _ := def.Metadata["identifier"].(string)
		kind, _ := def.Metadata["kind"].(string)
		if identifier == "" {
			continue
		}
		if pkgName == "" {
			pkgName, _ = def.Metadata["package_name"].(string)
		}

		// Prefer the structured signature; fall back to the first line of content.
		sig, _ := def.Metadata["signature"].(string)
		if sig == "" {
			sig = strings.SplitN(def.PageContent, "\n", 2)[0]
		}

		// Optionally include the first sentence of the doc comment so the
		// embedding captures semantic purpose, not just the symbol name.
		doc, _ := def.Metadata["documentation"].(string)
		firstSentence := ""
		if doc != "" {
			// Strip comment prefix characters and take up to the first period.
			cleaned := strings.TrimLeft(doc, "/ #*")
			cleaned = strings.TrimSpace(cleaned)
			if idx := strings.IndexByte(cleaned, '.'); idx > 0 {
				firstSentence = cleaned[:idx+1]
			} else if len(cleaned) < 120 {
				firstSentence = cleaned
			}
		}

		if firstSentence != "" {
			fmt.Fprintf(&sb, "%s (%s): %s — %s\n", identifier, kind, sig, firstSentence)
		} else {
			fmt.Fprintf(&sb, "%s (%s): %s\n", identifier, kind, sig)
		}
	}

	if pkgName != "" {
		// Insert "Package: X" after "File: X\n\n" (position 2 in lines)
		result := sb.String()
		insertAt := strings.Index(result, "\n\n")
		if insertAt >= 0 {
			result = result[:insertAt+2] + "Package: " + pkgName + "\n\n" + result[insertAt+2:]
			tocDoc := schema.NewDocument(result, map[string]any{
				"chunk_type":   "toc",
				"source":       relPath,
				"package_name": pkgName,
				"symbol_count": len(defDocs),
			})
			return &tocDoc
		}
	}

	tocDoc := schema.NewDocument(sb.String(), map[string]any{
		"chunk_type":   "toc",
		"source":       relPath,
		"symbol_count": len(defDocs),
	})
	return &tocDoc
}
