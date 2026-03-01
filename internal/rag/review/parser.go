package review

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"strings"

	"github.com/sevigo/goframe/output"

	"github.com/sevigo/code-warden/internal/core"
	internalgithub "github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/llm"
)

// StructuredReviewParser implements github.com/sevigo/goframe/schema OutputParser
// for parsing code reviews output by LLMs.
type StructuredReviewParser struct {
	logger *slog.Logger
	Raw    string
}

// NewStructuredReviewParser creates a new StructuredReviewParser.
func NewStructuredReviewParser(logger *slog.Logger) *StructuredReviewParser {
	return &StructuredReviewParser{logger: logger}
}

// Parse extracts the structured review from the LLM output.
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

// ParseDiff splits a unified diff string into per-file [internalgithub.ChangedFile] entries.
func ParseDiff(diff string) []internalgithub.ChangedFile {
	var files []internalgithub.ChangedFile
	var currentFile *internalgithub.ChangedFile

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
				currentFile = &internalgithub.ChangedFile{
					Filename: filename,
				}
			}
		case strings.HasPrefix(line, "@@"):
			// Hunk header — skip, not part of the patch body
			continue
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

// SanitizeModelForFilename converts a model name into a safe filename component.
func SanitizeModelForFilename(modelName string) string {
	sanitized := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		if r == '-' || r == '.' {
			return r
		}
		return '_'
	}, modelName)

	// De-duplicate underscores
	for strings.Contains(sanitized, "__") {
		sanitized = strings.ReplaceAll(sanitized, "__", "_")
	}

	sanitized = strings.Trim(sanitized, "_")
	if sanitized == "" {
		sanitized = "model"
	}

	// Add a short hash to prevent name collisions
	h := sha256.New()
	h.Write([]byte(modelName))
	hashStr := hex.EncodeToString(h.Sum(nil))[:16]

	// Handle Windows reserved names
	reserved := map[string]bool{
		"CON": true, "PRN": true, "AUX": true, "NUL": true,
		"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
		"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
	}

	base := sanitized
	if dot := strings.LastIndex(base, "."); dot > 0 {
		base = base[:dot]
	}

	if reserved[strings.ToUpper(base)] {
		sanitized = "safe_" + sanitized
	}

	// Append hash and limit length
	fullName := sanitized + "_" + hashStr
	if len(fullName) > 120 {
		fullName = fullName[:120]
	}

	return fullName
}
