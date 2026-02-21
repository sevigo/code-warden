package rag

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/sevigo/goframe/embeddings/sparse"
	"github.com/sevigo/goframe/schema"
	"github.com/sevigo/goframe/vectorstores"

	"github.com/sevigo/code-warden/internal/core"
	internalgithub "github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/llm"
	"github.com/sevigo/code-warden/internal/storage"
)

// GenerateReReview performs feedback-driven retrieval to find relevant context
// for verifying fixes to previously identified issues.
func (r *ragService) GenerateReReview(ctx context.Context, repo *storage.Repository, event *core.GitHubEvent, originalReview *core.Review, ghClient internalgithub.Client, changedFiles []internalgithub.ChangedFile) (*core.StructuredReview, string, error) {
	r.logger.Info("preparing data for a re-review", "repo", event.RepoFullName, "pr", event.PRNumber)

	newDiff, err := ghClient.GetPullRequestDiff(ctx, event.RepoOwner, event.RepoName, event.PRNumber)
	if err != nil {
		return nil, "", fmt.Errorf("failed to get new PR diff: %w", err)
	}
	if strings.TrimSpace(newDiff) == "" {
		r.logger.Info("no new code changes found to re-review", "pr", event.PRNumber)
		return &core.StructuredReview{
			Summary: "This pull request contains no new code changes to re-review.",
		}, "This pull request contains no new code changes to re-review.", nil
	}

	// Step 1: Build standard context (Arch + Impact + HyDE + Definitions) for the changed files
	standardContext, definitionsContext := r.buildRelevantContext(ctx, repo.QdrantCollectionName, repo.EmbedderModelName, repo.ClonePath, changedFiles, event.PRTitle+"\n"+event.PRBody)

	// Step 2: Extract feedback-driven search queries from original review
	feedbackQueries := r.extractCommentsFromReview(ctx, originalReview.ReviewContent)
	r.logger.Info("extracted feedback-driven search queries", "count", len(feedbackQueries))

	// Step 3: Perform feedback-driven vector searches
	feedbackContext := r.buildFeedbackDrivenContext(ctx, repo.QdrantCollectionName, repo.EmbedderModelName, feedbackQueries, event.UserInstructions)

	// Step 4: Combine contexts with feedback-driven results prioritized
	combinedContext := r.combineReReviewContext(standardContext, feedbackContext)

	promptData := core.ReReviewData{
		Language:         event.Language,
		OriginalReview:   originalReview.ReviewContent,
		NewDiff:          newDiff,
		UserInstructions: event.UserInstructions,
		Context:          combinedContext,
		Definitions:      definitionsContext,
	}

	rawReview, err := r.generateResponseWithPrompt(ctx, event, llm.ReReviewPrompt, promptData)
	if err != nil {
		return nil, "", err
	}

	structuredReview, err := llm.ParseMarkdownReview(ctx, rawReview, r.logger)
	if err != nil {
		r.logger.Warn("failed to parse re-review, using raw output", "error", err)
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

// buildFeedbackDrivenContext performs vector searches using the extracted feedback
// comments as queries to find relevant code definitions and dependencies.
func (r *ragService) buildFeedbackDrivenContext(ctx context.Context, collectionName, embedderModelName string, feedbackQueries []string, userInstructions string) string {
	if len(feedbackQueries) == 0 && userInstructions == "" {
		return ""
	}

	scopedStore := r.vectorStore.ForRepo(collectionName, embedderModelName)
	seenDocs := make(map[string]struct{})
	var mu sync.Mutex
	var wg sync.WaitGroup

	// Channel to collect results from goroutines
	resultChan := make(chan string, len(feedbackQueries)+1)

	// Limit concurrent searches to prevent resource exhaustion
	const maxConcurrentSearches = 10
	semaphore := make(chan struct{}, maxConcurrentSearches)

	// Search for each feedback query
	for _, query := range feedbackQueries {
		if strings.TrimSpace(query) == "" {
			continue
		}
		wg.Add(1)
		semaphore <- struct{}{} // Acquire slot
		go func(q string) {
			defer func() { <-semaphore }() // Release slot
			r.performReReviewSearch(ctx, scopedStore, q, "feedback query", "Relevant to", resultChan, seenDocs, &mu, &wg)
		}(query)
	}

	// If user provided specific instructions, search for those too
	if userInstructions != "" {
		wg.Add(1)
		semaphore <- struct{}{} // Acquire slot
		go func() {
			defer func() { <-semaphore }() // Release slot
			r.performReReviewSearch(ctx, scopedStore, userInstructions, "user instructions", "Relevant to user focus", resultChan, seenDocs, &mu, &wg)
		}()
	}

	// Wait for all searches to complete, then close the channel
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	// Collect all results into the builder (single goroutine - no race condition)
	var contextBuilder strings.Builder
	contextBuilder.WriteString("# Feedback-Driven Context\n\n")
	contextBuilder.WriteString("The following code definitions and dependencies are relevant to the issues raised in the original review:\n\n")

	for result := range resultChan {
		contextBuilder.WriteString(result)
	}

	r.logger.Info("feedback-driven context built", "queries", len(feedbackQueries), "unique_docs", len(seenDocs))

	if len(seenDocs) == 0 {
		return ""
	}

	return contextBuilder.String()
}

// performReReviewSearch executes a single search query (either from feedback or user instructions)
// and handles deduplication and result formatting for the re-review context.
func (r *ragService) performReReviewSearch(ctx context.Context, scopedStore storage.ScopedVectorStore, query, queryType, headerPrefix string, resultChan chan<- string, seenDocs map[string]struct{}, mu *sync.Mutex, wg *sync.WaitGroup) {
	defer wg.Done()

	// Check for cancellation before starting work
	select {
	case <-ctx.Done():
		return
	default:
	}

	if queryType == "user instructions" {
		r.logger.Info("performing targeted search for user instructions", "instructions", query)
	}

	docs := r.performSearch(ctx, scopedStore, query, queryType)
	if len(docs) == 0 {
		r.logger.Debug("no documents found for query", "query", query[:min(50, len(query))], "type", queryType)
		return
	}

	var builder strings.Builder

	for _, doc := range docs {
		// Check for cancellation during processing
		select {
		case <-ctx.Done():
			return
		default:
		}

		docKey := r.getDocKey(doc)

		mu.Lock()
		if _, exists := seenDocs[docKey]; exists {
			mu.Unlock()
			continue
		}
		seenDocs[docKey] = struct{}{}
		mu.Unlock()

		source, _ := doc.Metadata["source"].(string)
		content := r.getDocContent(doc)

		_, _ = fmt.Fprintf(&builder, "## %s: %s\n", headerPrefix, source)
		builder.WriteString("```\n")
		builder.WriteString(content)
		builder.WriteString("\n```\n\n")
	}

	if builder.Len() > 0 {
		resultChan <- builder.String()
	}
}

// performSearch executes a similarity search with sparse vector fallback.
func (r *ragService) performSearch(ctx context.Context, scopedStore storage.ScopedVectorStore, query, queryType string) []schema.Document {
	var searchOpts []vectorstores.Option
	sparseVec, err := sparse.GenerateSparseVector(ctx, query)
	if err != nil {
		r.logger.Debug("sparse vector generation failed, using dense only", "query", query[:min(50, len(query))], "error", err)
	} else {
		searchOpts = append(searchOpts, vectorstores.WithSparseQuery(sparseVec))
	}

	docs, err := scopedStore.SimilaritySearch(ctx, query, 5, searchOpts...)
	if err != nil {
		r.logger.Warn("search failed", "queryType", queryType, "query", query[:min(50, len(query))], "error", err)
		return nil
	}

	return docs
}

// combineReReviewContext merges standard context with feedback-driven context,
// prioritizing feedback-driven results at the beginning.
func (r *ragService) combineReReviewContext(standardContext, feedbackContext string) string {
	if feedbackContext == "" {
		return standardContext
	}

	var result strings.Builder
	result.WriteString(feedbackContext)
	result.WriteString("\n---\n\n")
	result.WriteString(standardContext)

	return result.String()
}

// extractCommentsFromReview parses the original review content and extracts
// all <comment> tag contents to use as feedback-driven search queries.
func (r *ragService) extractCommentsFromReview(ctx context.Context, reviewContent string) []string {
	var queries []string

	// Use the existing extractMultipleTags helper from parser.go
	suggestionBlocks := llm.ExtractMultipleTags(ctx, reviewContent, "suggestion")

	for _, block := range suggestionBlocks {
		if comment, ok := llm.ExtractTag(block, "comment"); ok && strings.TrimSpace(comment) != "" {
			// Clean up the comment for use as a search query
			cleaned := r.cleanCommentForQuery(comment)
			if cleaned != "" {
				queries = append(queries, cleaned)
			}
		}
	}

	return queries
}

// cleanCommentForQuery prepares a comment for use as a vector search query.
// It normalizes markdown artifacts and structural headers while preserving semantic context.
func (r *ragService) cleanCommentForQuery(comment string) string {
	// Remove markdown formatting (backticks)
	comment = strings.ReplaceAll(comment, "```", " ")
	comment = strings.ReplaceAll(comment, "`", " ")

	// Normalize structural headers to separators for semantic retention
	// This preserves the semantic meaning (e.g., "Observation:", "Fix:") while reducing noise.
	// Status markers are removed as they don't contribute to code retrieval.
	comment = statusRegex.ReplaceAllString(comment, " ")
	comment = obsRegex.ReplaceAllString(comment, " | Observation: ")
	comment = rootCauseRegex.ReplaceAllString(comment, " | Root Cause: ")
	comment = fixRegex.ReplaceAllString(comment, " | Fix: ")

	// Collapse multiple spaces and trim
	comment = whitespaceRegex.ReplaceAllString(comment, " ")
	comment = strings.TrimSpace(comment)

	// Limit query length to avoid overly long searches
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
