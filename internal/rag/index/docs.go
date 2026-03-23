package index

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/sevigo/goframe/embeddings/sparse"
	"github.com/sevigo/goframe/schema"
)

// setDocsMetadata sets metadata for documentation chunks.
func (i *Indexer) setDocsMetadata(doc *schema.Document, file string, isReadme, isRootReadme bool) {
	doc.Metadata["chunk_type"] = "docs"
	doc.Metadata["language"] = "markdown"
	doc.Metadata["directory"] = filepath.Dir(file)

	if isReadme {
		doc.Metadata["is_readme"] = true
		if isRootReadme {
			doc.Metadata["weight"] = 2.0 // Root README gets highest weight
		} else {
			doc.Metadata["weight"] = 1.5 // Subdirectory README gets higher weight
		}
	} else {
		doc.Metadata["weight"] = 1.0
	}
}

// ProcessDocsFile processes documentation files (README.md, etc.) for indexing.
// Documentation chunks get higher retrieval weight and special handling.
func (i *Indexer) ProcessDocsFile(ctx context.Context, repoPath, file string) []schema.Document {
	fullPath := filepath.Join(repoPath, file)

	contentBytes, err := os.ReadFile(fullPath)
	if err != nil {
		i.cfg.Logger.Error("failed to read docs file", "file", file, "error", err)
		return nil
	}

	content := strings.ToValidUTF8(string(contentBytes), "")

	// Check if this is a root-level README (higher priority)
	isRootReadme := filepath.Dir(file) == "." || filepath.Dir(file) == ""
	isReadme := strings.EqualFold(filepath.Base(file), "readme.md") ||
		strings.EqualFold(filepath.Base(file), "readme") ||
		strings.EqualFold(filepath.Base(file), "readme.markdown")

	// Create document with docs metadata
	doc := schema.NewDocument(content, map[string]any{
		"source":     file,
		"chunk_type": "docs",
		"language":   "markdown",
		"is_readme":  isReadme,
		"is_root":    isRootReadme,
		"weight":     getDocsWeight(isRootReadme, isReadme),
		"file_name":  filepath.Base(file),
		"directory":  filepath.Dir(file),
	})

	// For READMEs, add the directory context
	if isReadme && !isRootReadme {
		dirName := filepath.Base(filepath.Dir(file))
		doc.PageContent = "Directory: " + dirName + "\n\n" + content
	}

	// Generate sparse vector for hybrid search
	sparseVec, err := sparse.GenerateSparseVector(ctx, doc.PageContent)
	if err == nil {
		doc.Sparse = sparseVec
	}

	// Generate summary for docs as well (helps with retrieval)
	if i.cfg.LLM != nil && i.cfg.PromptMgr != nil && len(content) > 100 {
		summary := i.generateDocsSummary(ctx, file, content)
		if summary != "" {
			doc.PageContent = doc.PageContent + "\n\n[Summary: " + summary + "]"
			doc.Metadata["docs_summary"] = summary
		}
	}

	docs := []schema.Document{doc}

	// For very large docs, split into sections
	if len(content) > 8000 {
		sectionDocs := i.splitDocsIntoSections(ctx, file, content, isReadme, isRootReadme)
		if len(sectionDocs) > 0 {
			docs = append(docs, sectionDocs...)
		}
	}

	return docs
}

// getDocsWeight returns the retrieval weight for documentation chunks.
// Root READMEs get highest weight, regular READMEs get medium weight.
func getDocsWeight(isRoot, isReadme bool) float64 {
	if isRoot && isReadme {
		return 2.0 // Highest priority - root README
	}
	if isReadme {
		return 1.5 // Medium-high priority - subdirectory README
	}
	return 1.0 // Normal priority - other docs
}

// splitDocsIntoSections splits large documentation into section-based chunks.
func (i *Indexer) splitDocsIntoSections(ctx context.Context, file, content string, isReadme, isRoot bool) []schema.Document {
	sections := splitMarkdownByHeaders(content)
	if len(sections) <= 1 {
		return nil
	}

	var docs []schema.Document
	for i, section := range sections {
		if len(section.content) < 100 {
			continue // Skip very short sections
		}

		doc := schema.NewDocument(section.content, map[string]any{
			"source":        file,
			"chunk_type":    "docs_section",
			"language":      "markdown",
			"is_readme":     isReadme,
			"is_root":       isRoot,
			"weight":        getDocsWeight(isRoot, isReadme),
			"section_index": i,
			"section_title": section.title,
			"directory":     filepath.Dir(file),
		})

		// Add context about the section
		if section.title != "" {
			doc.PageContent = "## " + section.title + "\n\n" + section.content
		}

		// Sparse vector
		sparseVec, err := sparse.GenerateSparseVector(ctx, doc.PageContent)
		if err == nil {
			doc.Sparse = sparseVec
		}

		docs = append(docs, doc)
	}

	return docs
}

// generateDocsSummary creates a brief summary for a documentation file.
func (i *Indexer) generateDocsSummary(ctx context.Context, _ string, content string) string {
	if i.cfg.LLM == nil || i.cfg.PromptMgr == nil {
		return ""
	}

	contentHash := hashContent(content)
	globalFileSummaryCache.mu.RLock()
	if result, ok := globalFileSummaryCache.cache[contentHash]; ok {
		globalFileSummaryCache.mu.RUnlock()
		return result.summary
	}
	globalFileSummaryCache.mu.RUnlock()

	summary := i.generateInlineSummary(ctx, content)
	if summary == "" {
		return ""
	}

	result := fileSummaryResult{summary: summary}
	globalFileSummaryCache.mu.Lock()
	globalFileSummaryCache.cache[contentHash] = result
	globalFileSummaryCache.mu.Unlock()

	return summary
}

// generateInlineSummary creates a summary without a dedicated prompt file.
func (i *Indexer) generateInlineSummary(_ context.Context, content string) string {
	// Extract first significant paragraph or section
	lines := strings.Split(content, "\n")
	var summaryLines []string
	inCodeBlock := false

	for _, line := range lines {
		if strings.HasPrefix(line, "```") {
			inCodeBlock = !inCodeBlock
			continue
		}
		if inCodeBlock {
			continue
		}
		// Skip empty lines and headers at the start
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			if len(summaryLines) > 0 {
				break // Got enough
			}
			continue
		}
		summaryLines = append(summaryLines, line)
		if len(summaryLines) >= 3 {
			break
		}
	}

	if len(summaryLines) == 0 {
		return ""
	}

	summary := strings.Join(summaryLines, " ")
	if len(summary) > 200 {
		summary = summary[:197] + "..."
	}

	return summary
}

// markdownSection represents a section in a markdown document.
type markdownSection struct {
	title   string
	content string
}

// splitMarkdownByHeaders splits markdown content into sections by headers.
func splitMarkdownByHeaders(content string) []markdownSection {
	var sections []markdownSection
	lines := strings.Split(content, "\n")

	var currentSection markdownSection
	var currentLines []string

	for _, line := range lines {
		if strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "# ") {
			// Save previous section
			if len(currentLines) > 0 {
				currentSection.content = strings.Join(currentLines, "\n")
				sections = append(sections, currentSection)
			}
			// Start new section
			currentSection = markdownSection{
				title: strings.TrimSpace(strings.TrimLeft(line, "# ")),
			}
			currentLines = nil
		} else {
			currentLines = append(currentLines, line)
		}
	}

	// Save last section
	if len(currentLines) > 0 {
		currentSection.content = strings.Join(currentLines, "\n")
		sections = append(sections, currentSection)
	}

	return sections
}
