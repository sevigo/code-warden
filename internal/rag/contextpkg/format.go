package contextpkg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/sevigo/goframe/embeddings/sparse"
	"github.com/sevigo/goframe/schema"

	internalgithub "github.com/sevigo/code-warden/internal/github"
)

// BuildContextForPrompt formats retrieved documents into a prompt-ready string.
func (b *builderImpl) BuildContextForPrompt(docs []schema.Document) string {
	if len(docs) == 0 {
		return ""
	}

	seenDocs := make(map[string]struct{})
	unique := make([]schema.Document, 0, len(docs))
	for _, doc := range docs {
		key := b.getDocKey(doc)
		if _, exists := seenDocs[key]; exists {
			continue
		}
		seenDocs[key] = struct{}{}
		unique = append(unique, doc)
	}

	type fileEntry struct {
		source string
		docs   []schema.Document
	}
	order := make([]string, 0, len(unique))
	groups := make(map[string]*fileEntry)
	for _, doc := range unique {
		source, _ := doc.Metadata["source"].(string)
		if _, seen := groups[source]; !seen {
			order = append(order, source)
			groups[source] = &fileEntry{source: source}
		}
		groups[source].docs = append(groups[source].docs, doc)
	}

	var contextBuilder strings.Builder
	for _, src := range order {
		entry := groups[src]
		contextBuilder.WriteString("---\n")
		fmt.Fprintf(&contextBuilder, "File: %s\n", src)

		first := entry.docs[0]
		if pkg, ok := first.Metadata["package_name"].(string); ok && pkg != "" {
			fmt.Fprintf(&contextBuilder, "Package: %s\n", pkg)
		}
		if identifier, _ := first.Metadata["identifier"].(string); identifier != "" {
			if parentID, _ := first.Metadata["parent_id"].(string); parentID == "" {
				fmt.Fprintf(&contextBuilder, "Identifier: %s\n", identifier)
			}
		}

		contextBuilder.WriteString("\n")
		contextBuilder.WriteString(b.mergeChunksForFile(entry.docs))
		contextBuilder.WriteString("\n---\n\n")
	}
	return contextBuilder.String()
}

func (b *builderImpl) mergeChunksForFile(docs []schema.Document) string {
	if len(docs) == 1 {
		return b.getDocContent(docs[0])
	}

	first := b.getDocContent(docs[0])
	var merged strings.Builder
	merged.WriteString(first)

	const maxOverlapTail = 300
	tail := first
	if len(tail) > maxOverlapTail {
		tail = tail[len(tail)-maxOverlapTail:]
	}

	for i := 1; i < len(docs); i++ {
		curr := b.getDocContent(docs[i])
		overlapStart := findOverlapStart(tail, curr)
		if overlapStart > 0 {
			merged.WriteString(curr[overlapStart:])
		} else {
			merged.WriteString("\n")
			merged.WriteString(curr)
		}

		if len(curr) >= maxOverlapTail {
			tail = curr[len(curr)-maxOverlapTail:]
		} else {
			tail += curr
			if len(tail) > maxOverlapTail {
				tail = tail[len(tail)-maxOverlapTail:]
			}
		}
	}
	return merged.String()
}

func findOverlapStart(prev, curr string) int {
	const maxOverlap = 300
	overlap := len(prev)
	if overlap > maxOverlap {
		overlap = maxOverlap
		prev = prev[len(prev)-overlap:]
	}
	if overlap > len(curr) {
		overlap = len(curr)
	}
	for size := overlap; size >= 10; size-- {
		if strings.HasSuffix(prev, curr[:size]) {
			return size
		}
	}
	return 0
}

func (b *builderImpl) fallbackConcat(docs []schema.Document) string {
	const charsPerToken = 4
	maxChars := b.cfg.AIConfig.ContextTokenBudget * charsPerToken
	if maxChars <= 0 {
		maxChars = 64000
	}

	var fallback strings.Builder
	currentChars := 0

	for _, doc := range docs {
		docLen := len(doc.PageContent)
		if currentChars+docLen > maxChars {
			break
		}
		fallback.WriteString(doc.PageContent)
		fallback.WriteString("\n---\n\n")
		currentChars += docLen + 5
	}

	return fallback.String()
}

func (b *builderImpl) buildContextDocuments(arch, toc, fileSummary, impact, description, definitions, testCoverage string, hyde [][]schema.Document, indices []int, files []internalgithub.ChangedFile) []schema.Document {
	var docs []schema.Document
	if definitions != "" {
		docs = append(docs, schema.Document{PageContent: definitions})
	}
	// TOC chunks give the LLM a guaranteed symbol inventory for each changed
	// file — what it exports, kinds, and doc summaries — regardless of what
	// semantic search returns for those files.
	if toc != "" {
		docs = append(docs, schema.Document{PageContent: fmt.Sprintf("# Changed File Inventories\n\nThe following table-of-contents entries list every exported symbol in each changed file:\n\n%s", toc)})
	}
	// File summaries provide high-level description of each changed file
	if fileSummary != "" {
		docs = append(docs, schema.Document{PageContent: fmt.Sprintf("# Changed File Summaries\n\nThe following describes what each changed file does:\n\n%s", fileSummary)})
	}
	if testCoverage != "" {
		docs = append(docs, schema.Document{PageContent: testCoverage})
	}
	if description != "" {
		docs = append(docs, schema.Document{PageContent: description})
	}
	if impact != "" {
		docs = append(docs, schema.Document{PageContent: fmt.Sprintf("# Potential Impacted Callers & Usages\n\nThe following code snippets may be affected by the changes in modified symbols:\n\n%s", impact)})
	}
	if arch != "" {
		docs = append(docs, schema.Document{PageContent: fmt.Sprintf("# Architectural Context\n\nThe following describes the purpose of the affected modules:\n\n%s", arch)})
	}
	if hydeContent := b.buildHyDEContent(hyde, indices, files); hydeContent != "" {
		docs = append(docs, schema.Document{PageContent: hydeContent})
	}
	return docs
}

func (b *builderImpl) getDocKey(doc schema.Document) string {
	source, _ := doc.Metadata["source"].(string)
	identifier, _ := doc.Metadata["identifier"].(string)
	parentID, ok := doc.Metadata["parent_id"].(string)
	if ok && parentID != "" {
		return parentID
	}
	if identifier != "" && source != "" {
		return fmt.Sprintf("%s-%s", source, identifier)
	}
	if source != "" {
		return source
	}
	h := sha256.Sum256([]byte(doc.PageContent))
	return hex.EncodeToString(h[:])
}

func (b *builderImpl) getDocContent(doc schema.Document) string {
	if parentText, ok := doc.Metadata["full_parent_text"].(string); ok && parentText != "" {
		return parentText
	}
	if parentID, ok := doc.Metadata["parent_id"].(string); ok && parentID != "" {
		b.cfg.Logger.Debug("parent_id present but full_parent_text missing", "parent_id", parentID, "source", doc.Metadata["source"])
	}
	return doc.PageContent
}

// filterTestDocs removes documents originating from test files (is_test: true).
// Test code pollutes the review context with patterns like mock setup and assertion
// helpers that are irrelevant to production code review.
func filterTestDocs(docs []schema.Document) []schema.Document {
	filtered := make([]schema.Document, 0, len(docs))
	for _, doc := range docs {
		if isTest, _ := doc.Metadata["is_test"].(bool); !isTest {
			filtered = append(filtered, doc)
		}
	}
	return filtered
}

func mergeAndDedup(docs []schema.Document, keyFn func(schema.Document) string) []schema.Document {
	seen := make(map[string]schema.Document, len(docs))
	for _, d := range docs {
		key := keyFn(d)
		if _, exists := seen[key]; !exists {
			seen[key] = d
		}
	}
	unique := make([]schema.Document, 0, len(seen))
	for _, d := range seen {
		unique = append(unique, d)
	}
	sort.Slice(unique, func(i, j int) bool {
		si, _ := unique[i].Metadata["source"].(string)
		sj, _ := unique[j].Metadata["source"].(string)
		return si < sj
	})
	return unique
}

func (b *builderImpl) splitAndFormatDocs(ctx context.Context, allDocs []schema.Document, descDocs []schema.Document, prDescription string, seen *sync.Map) (string, string) {
	descKeys := make(map[string]schema.Document, len(descDocs))
	for _, d := range descDocs {
		source, _ := d.Metadata["source"].(string)
		descKeys[source] = d
	}

	validDescSources := b.filterValidDescriptionDocs(ctx, descKeys, seen, prDescription)
	return b.formatSplitDocs(allDocs, descKeys, validDescSources, seen, prDescription)
}

func (b *builderImpl) formatSplitDocs(allDocs []schema.Document, descKeys map[string]schema.Document, validDescSources map[string]bool, seen *sync.Map, prDescription string) (string, string) {
	var impactBuilder, descBuilder strings.Builder
	for _, doc := range allDocs {
		source, _ := doc.Metadata["source"].(string)

		if _, isDesc := descKeys[source]; isDesc && prDescription != "" {
			if !validDescSources[source] {
				continue
			}
		}

		if _, loaded := seen.LoadOrStore(source, struct{}{}); loaded {
			continue
		}

		content := b.getDocContent(doc)
		escapedContent := escapeCodeFences(content)
		if _, isDesc := descKeys[source]; isDesc && prDescription != "" {
			fmt.Fprintf(&descBuilder, "File: %s\n```\n%s\n```\n\n", source, escapedContent)
		} else {
			fmt.Fprintf(&impactBuilder, "**%s**:\n```\n%s\n```\n\n", source, escapedContent)
		}
	}

	var descCtx string
	if descBuilder.Len() > 0 {
		descCtx = "# Related to PR Description\n\n" + descBuilder.String()
	}
	return impactBuilder.String(), descCtx
}

// generateSparseVectorFunc returns a function mapping a list of queries to sparse vectors.
// Queries that fail validation are filtered out before generation to avoid nil pointer crashes.
func (b *builderImpl) generateSparseVectorFunc(stageName string) func(ctx context.Context, queries []string) ([]*schema.SparseVector, error) {
	return func(ctx context.Context, queries []string) ([]*schema.SparseVector, error) {
		// Filter out invalid queries that would fail sparse vector generation
		validQueries := make([]string, 0, len(queries))
		for _, q := range queries {
			if isValidQueryForSparse(q) {
				validQueries = append(validQueries, q)
			} else {
				b.cfg.Logger.Debug("skipping invalid query for sparse vector generation", "stage", stageName, "query", truncateForLog(q, 50))
			}
		}

		if len(validQueries) == 0 {
			b.cfg.Logger.Warn("all queries invalid for sparse vector generation, returning empty slice", "stage", stageName)
			return nil, nil
		}

		vecs := make([]*schema.SparseVector, len(validQueries))
		for i, q := range validQueries {
			v, err := sparse.GenerateSparseVector(ctx, q)
			if err != nil {
				b.cfg.Logger.Warn("failed to generate sparse vector, using dense only", "stage", stageName, "query", truncateForLog(q, 50), "error", err)
				vecs[i] = nil
				continue
			}
			vecs[i] = v
		}
		return vecs, nil
	}
}

// isValidQueryForSparse checks if a query is valid for sparse vector generation.
// Invalid queries: too short, code fragments, format strings, etc.
func isValidQueryForSparse(query string) bool {
	if query == "" {
		return false
	}
	// Minimum length for meaningful sparse vector
	if len(strings.TrimSpace(query)) < 5 {
		return false
	}
	// Reject code fragments that often fail tokenization
	lower := strings.ToLower(query)
	// Single tokens or fragments
	if strings.HasPrefix(lower, "return ") && len(query) < 20 {
		return false
	}
	// Format string artifacts
	if strings.Contains(query, "%s") || strings.Contains(query, "%d") {
		return false
	}
	// Single symbols or tokens
	if len(strings.Fields(query)) < 2 {
		return false
	}
	return true
}

// truncateForLog truncates a string for safe logging.
func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
