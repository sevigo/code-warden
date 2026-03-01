package review

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/sevigo/goframe/embeddings/sparse"
	"github.com/sevigo/goframe/schema"
	"github.com/sevigo/goframe/vectorstores"

	"github.com/sevigo/code-warden/internal/core"
	internalgithub "github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/llm"
	"github.com/sevigo/code-warden/internal/storage"
)

// Pre-compiled regexes for review comment cleaning.
var (
	statusRegex     = regexp.MustCompile(`(?i)\*\*status:\*\*\s*(unresolved|partial|fixed|new critical bug)\s*`)
	obsRegex        = regexp.MustCompile(`(?i)\*\*observation:\*\*`)
	rootCauseRegex  = regexp.MustCompile(`(?i)\*\*root cause:\*\*`)
	fixRegex        = regexp.MustCompile(`(?i)\*\*fix:\*\*`)
	whitespaceRegex = regexp.MustCompile(`\s+`)
)

func getDocKey(doc schema.Document) string {
	source, _ := doc.Metadata["source"].(string)
	lineStart, _ := doc.Metadata["lineStart"].(int)
	lineEnd, _ := doc.Metadata["lineEnd"].(int)
	return fmt.Sprintf("%s:%d-%d", source, lineStart, lineEnd)
}

func getDocContent(doc schema.Document) string {
	content := doc.PageContent
	if doc.Metadata["type"] == "signature" && len(content) > 500 {
		return content[:500] + "..."
	}
	return content
}

// GenerateReReview generates a follow-up review by comparing the new diff
// against the original review's suggestions, using feedback-driven retrieval.
func (s *Service) GenerateReReview(ctx context.Context, repo *storage.Repository, event *core.GitHubEvent, originalReview *core.Review, ghClient internalgithub.Client, changedFiles []internalgithub.ChangedFile) (*core.StructuredReview, string, error) {
	s.cfg.Logger.Info("preparing data for a re-review", "repo", event.RepoFullName, "pr", event.PRNumber)

	newDiff, err := ghClient.GetPullRequestDiff(ctx, event.RepoOwner, event.RepoName, event.PRNumber)
	if err != nil {
		return nil, "", fmt.Errorf("failed to get new PR diff: %w", err)
	}
	if strings.TrimSpace(newDiff) == "" {
		s.cfg.Logger.Info("no new code changes found to re-review", "pr", event.PRNumber)
		return &core.StructuredReview{
			Summary: "This pull request contains no new code changes to re-review.",
		}, "This pull request contains no new code changes to re-review.", nil
	}

	// Build standard context
	standardContext, definitionsContext := s.cfg.BuildContext(ctx, repo.QdrantCollectionName, repo.EmbedderModelName, repo.ClonePath, changedFiles, event.PRTitle+"\n"+event.PRBody)

	// Extract search queries from original review
	feedbackQueries := s.extractCommentsFromReview(ctx, originalReview.ReviewContent)
	s.cfg.Logger.Info("extracted feedback-driven search queries", "count", len(feedbackQueries))

	// Feedback-driven searches
	feedbackContext := s.buildFeedbackDrivenContext(ctx, repo.QdrantCollectionName, repo.EmbedderModelName, feedbackQueries, event.UserInstructions)

	// Combine contexts
	combinedContext := s.combineReReviewContext(standardContext, feedbackContext)

	promptData := core.ReReviewData{
		Language:         event.Language,
		OriginalReview:   originalReview.ReviewContent,
		NewDiff:          newDiff,
		UserInstructions: event.UserInstructions,
		Context:          combinedContext,
		Definitions:      definitionsContext,
	}

	rawReview, err := s.generateResponseWithPrompt(ctx, event, llm.ReReviewPrompt, promptData)
	if err != nil {
		return nil, "", err
	}

	parser := NewStructuredReviewParser(s.cfg.Logger)
	structuredReview, err := parser.Parse(ctx, rawReview)
	if err != nil {
		s.cfg.Logger.Warn("failed to parse legacy re-review, using raw output", "error", err)
		structuredReview = &core.StructuredReview{Summary: rawReview}
	}

	if structuredReview.Title == "" {
		structuredReview.Title = "🔄 Follow-up Review Summary"
	}
	if structuredReview.Verdict == "" {
		structuredReview.Verdict = core.VerdictComment
	}

	return structuredReview, rawReview, nil
}

// buildFeedbackDrivenContext performs vector searches based on original review comments.
func (s *Service) buildFeedbackDrivenContext(ctx context.Context, collectionName, embedderModelName string, feedbackQueries []string, userInstructions string) string {
	if len(feedbackQueries) == 0 && userInstructions == "" {
		return ""
	}

	if s.cfg.VectorStore == nil {
		s.cfg.Logger.Warn("vector store not configured; skipping feedback-driven search")
		return ""
	}
	scopedStore := s.cfg.VectorStore.ForRepo(collectionName, embedderModelName)
	seenDocs := make(map[string]struct{})
	var mu sync.Mutex
	var wg sync.WaitGroup

	resultChan := make(chan string, len(feedbackQueries)+1)

	const maxConcurrentSearches = 10
	semaphore := make(chan struct{}, maxConcurrentSearches)

	for _, query := range feedbackQueries {
		if strings.TrimSpace(query) == "" {
			continue
		}
		wg.Add(1)
		semaphore <- struct{}{}
		go func(q string) {
			defer func() { <-semaphore }()
			s.performReReviewSearch(ctx, scopedStore, q, "feedback query", "Relevant to", resultChan, seenDocs, &mu, &wg)
		}(query)
	}

	if userInstructions != "" {
		wg.Add(1)
		semaphore <- struct{}{}
		go func() {
			defer func() { <-semaphore }()
			s.performReReviewSearch(ctx, scopedStore, userInstructions, "user instructions", "Relevant to user focus", resultChan, seenDocs, &mu, &wg)
		}()
	}

	go func() {
		wg.Wait()
		close(resultChan)
	}()

	var contextBuilder strings.Builder
	contextBuilder.WriteString("# Feedback-Driven Context\n\n")
	contextBuilder.WriteString("The following code definitions and dependencies are relevant to the issues raised in the original review:\n\n")

	for result := range resultChan {
		contextBuilder.WriteString(result)
	}

	s.cfg.Logger.Info("feedback-driven context built", "queries", len(feedbackQueries), "unique_docs", len(seenDocs))

	if len(seenDocs) == 0 {
		return ""
	}

	return contextBuilder.String()
}

// performReReviewSearch executes a similarity search for a single re-review query.
func (s *Service) performReReviewSearch(ctx context.Context, scopedStore storage.ScopedVectorStore, query, queryType, headerPrefix string, resultChan chan<- string, seenDocs map[string]struct{}, mu *sync.Mutex, wg *sync.WaitGroup) {
	defer wg.Done()

	select {
	case <-ctx.Done():
		return
	default:
	}

	if queryType == "user instructions" {
		s.cfg.Logger.Info("performing targeted search for user instructions", "instructions", query)
	}

	docs := s.performSearch(ctx, scopedStore, query, queryType)
	if len(docs) == 0 {
		s.cfg.Logger.Debug("no documents found for query", "query", query[:min(50, len(query))], "type", queryType)
		return
	}

	var builder strings.Builder

	for _, doc := range docs {
		select {
		case <-ctx.Done():
			return
		default:
		}

		docKey := getDocKey(doc)

		mu.Lock()
		if _, exists := seenDocs[docKey]; exists {
			mu.Unlock()
			continue
		}
		seenDocs[docKey] = struct{}{}
		mu.Unlock()

		source, _ := doc.Metadata["source"].(string)
		content := getDocContent(doc)

		_, _ = fmt.Fprintf(&builder, "## %s: %s\n", headerPrefix, source)
		builder.WriteString("```\n")
		builder.WriteString(content)
		builder.WriteString("\n```\n\n")
	}

	if builder.Len() > 0 {
		resultChan <- builder.String()
	}
}

// performSearch executes a similarity search with sparse vector support and exponential backoff.
func (s *Service) performSearch(ctx context.Context, scopedStore storage.ScopedVectorStore, query, queryType string) []schema.Document {
	const maxRetries = 3
	const baseDelay = 500 * time.Millisecond

	var lastErr error
	for attempt := range maxRetries {
		select {
		case <-ctx.Done():
			s.cfg.Logger.Debug("search cancelled by context", "queryType", queryType)
			return nil
		default:
		}

		var searchOpts []vectorstores.Option
		sparseVec, err := sparse.GenerateSparseVector(ctx, query)
		if err != nil {
			s.cfg.Logger.Debug("sparse vector generation failed, using dense only", "query", query[:min(50, len(query))], "error", err)
		} else {
			searchOpts = append(searchOpts, vectorstores.WithSparseQuery(sparseVec))
		}

		docs, err := scopedStore.SimilaritySearch(ctx, query, 5, searchOpts...)
		if err == nil {
			s.cfg.Logger.Debug("re-review search complete", "queryType", queryType, "docs_found", len(docs), "attempt", attempt+1)
			return docs
		}

		lastErr = err
		s.cfg.Logger.Warn("re-review search failed, will retry",
			"queryType", queryType,
			"attempt", attempt+1,
			"max_retries", maxRetries,
			"error", err,
		)

		// Exponential backoff with jitter
		if attempt < maxRetries-1 {
			delay := baseDelay * time.Duration(1<<(attempt%30))
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(delay):
			}
		}
	}

	s.cfg.Logger.Warn("re-review search failed after all retries", "queryType", queryType, "error", lastErr)
	return nil
}

// combineReReviewContext merges standard and feedback-driven context blocks.
func (s *Service) combineReReviewContext(standardContext, feedbackContext string) string {
	if feedbackContext == "" {
		return standardContext
	}

	var result strings.Builder
	result.WriteString(feedbackContext)
	result.WriteString("\n---\n\n")
	result.WriteString(standardContext)

	return result.String()
}

// extractCommentsFromReview parses review content to extract search queries
// from each suggestion's comment field.
func (s *Service) extractCommentsFromReview(ctx context.Context, reviewContent string) []string {
	var queries []string

	parser := NewStructuredReviewParser(s.cfg.Logger)
	parsedReview, err := parser.Parse(ctx, reviewContent)
	if err != nil {
		s.cfg.Logger.Warn("extractCommentsFromReview: failed to parse review", "error", err)
		return queries
	}

	for _, sug := range parsedReview.Suggestions {
		if strings.TrimSpace(sug.Comment) != "" {
			cleaned := s.cleanCommentForQuery(sug.Comment)
			if cleaned != "" {
				queries = append(queries, cleaned)
			}
		}
	}

	return queries
}

// cleanCommentForQuery strips formatting artifacts and truncates a comment for use as a search query.
func (s *Service) cleanCommentForQuery(comment string) string {
	comment = strings.ReplaceAll(comment, "```", " ")
	comment = strings.ReplaceAll(comment, "`", " ")

	comment = statusRegex.ReplaceAllString(comment, " ")
	comment = obsRegex.ReplaceAllString(comment, " | Observation: ")
	comment = rootCauseRegex.ReplaceAllString(comment, " | Root Cause: ")
	comment = fixRegex.ReplaceAllString(comment, " | Fix: ")

	comment = whitespaceRegex.ReplaceAllString(comment, " ")
	comment = strings.TrimSpace(comment)

	const maxQueryLen = 500
	if len(comment) > maxQueryLen {
		if idx := strings.LastIndex(comment[:maxQueryLen], " "); idx > 400 {
			comment = comment[:idx]
		} else {
			comment = comment[:maxQueryLen]
		}
	}

	return comment
}
