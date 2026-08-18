package review

import (
	"context"
	"log/slog"

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
