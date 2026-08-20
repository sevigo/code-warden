package review

import (
	"context"
	"log/slog"
	"strings"

	"github.com/sevigo/goframe/output"

	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/llm"
)

// StructuredReviewParser parses the <review> XML block emitted by an agent
// pass. It implements github.com/sevigo/goframe/schema OutputParser.
type StructuredReviewParser struct {
	logger *slog.Logger
	Raw    string
}

// NewStructuredReviewParser creates a new StructuredReviewParser.
func NewStructuredReviewParser(logger *slog.Logger) *StructuredReviewParser {
	return &StructuredReviewParser{logger: logger}
}

// Parse extracts the structured review from the agent's final response.
func (p *StructuredReviewParser) Parse(ctx context.Context, outputStr string) (*core.StructuredReview, error) {
	p.Raw = outputStr
	xmlParser := output.NewXMLParser[*core.StructuredReview]("review")
	parsed, err := xmlParser.Parse(ctx, outputStr)
	if err != nil {
		p.logger.Warn("failed to parse XML review, trying manual tag extraction", "error", err)
		return llm.ParseLegacyMarkdownReview(outputStr)
	}
	return parsed, nil
}

// ParseDiff splits a unified diff string into per-file [core.ChangedFile] entries.
func ParseDiff(diff string) []core.ChangedFile {
	var files []core.ChangedFile
	var currentFile *core.ChangedFile

	lines := strings.Split(diff, "\n")
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			// Start of a new file
			if currentFile != nil {
				files = append(files, *currentFile)
			}
			parts := strings.Fields(line)
			if len(parts) >= 4 {
				// Format: diff --git a/path/to/file b/path/to/file
				// We want the path after b/
				filename := strings.TrimPrefix(parts[3], "b/")
				currentFile = &core.ChangedFile{
					Filename: filename,
				}
			}
		case strings.HasPrefix(line, "@@"):
			// Hunk header — include it in the patch body so downstream
			// parsers (BuildValidLineMap / ParseValidLinesFromPatch) can
			// determine the new-side starting line number.
			if currentFile != nil {
				currentFile.Patch += line + "\n"
			}
		case strings.HasPrefix(line, "--- "), strings.HasPrefix(line, "+++ "):
			// Diff file headers — skip, not part of the patch body
			continue
		case currentFile != nil:
			// Append line to current file patch
			currentFile.Patch += line + "\n"
		}
	}

	if currentFile != nil {
		files = append(files, *currentFile)
	}

	return files
}
