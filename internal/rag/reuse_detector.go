package rag

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/sevigo/goframe/llms"
	"github.com/sevigo/goframe/vectorstores"
	"golang.org/x/sync/errgroup"

	internalgithub "github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/storage"
)

// ReuseSuggestion represents a hint that a newly written function might be reinventing the wheel.
type ReuseSuggestion struct {
	FilePath string
	Line     int
	Message  string
}

// ReuseDetector is responsible for detecting wheel reinvention in PRs.
type ReuseDetector interface {
	Detect(ctx context.Context, repo *storage.Repository, changedFiles []internalgithub.ChangedFile) ([]ReuseSuggestion, error)
}

type reuseDetector struct {
	llmModel llms.Model
	store    storage.VectorStore
}

// NewReuseDetector creates a new instance of ReuseDetector.
func NewReuseDetector(llmModel llms.Model, store storage.VectorStore) ReuseDetector {
	return &reuseDetector{
		llmModel: llmModel,
		store:    store,
	}
}

// addedFunction represents a newly added function in the diff.
type addedFunction struct {
	File string
	Line int
	Code string
}

func (d *reuseDetector) Detect(ctx context.Context, repo *storage.Repository, changedFiles []internalgithub.ChangedFile) ([]ReuseSuggestion, error) {
	functions := d.extractAddedFunctions(changedFiles)
	if len(functions) == 0 {
		return nil, nil // Nothing to do
	}

	scopedStore := d.store.ForRepo(repo.QdrantCollectionName, repo.EmbedderModelName)

	var suggestions []ReuseSuggestion
	var mu sync.Mutex

	g, ctx := errgroup.WithContext(ctx)
	// Limit concurrency to prevent overloading the LLM or VectorStore
	g.SetLimit(10)

	for _, fn := range functions {
		fn := fn // Capture loop variable
		g.Go(func() error {
			suggestion, err := d.processFunction(ctx, scopedStore, fn)
			if err != nil {
				// We don't want one failure to fail the whole detection, so we log and continue.
				// For the purpose of errgroup, returning nil means it won't cancel the context.
				return nil
			}
			if suggestion != nil {
				mu.Lock()
				suggestions = append(suggestions, *suggestion)
				mu.Unlock()
			}
			return nil
		})
	}

	// This errgroup basically ignores errors from processFunction to be resilient,
	// but Wait still needs to be called to wait for all goroutines to finish.
	if err := g.Wait(); err != nil {
		return nil, err
	}

	return suggestions, nil
}

// processFunction handles the intent extraction, retrieval, and verification for a single function.
func (d *reuseDetector) processFunction(ctx context.Context, scopedStore storage.ScopedVectorStore, fn addedFunction) (*ReuseSuggestion, error) {
	// 1. Intent Extraction
	query, err := d.extractIntent(ctx, fn.Code)
	if err != nil {
		return nil, fmt.Errorf("failed to extract intent: %w", err)
	}

	// 2. Retrieval
	docs, err := scopedStore.SimilaritySearch(ctx, query, 3,
		vectorstores.WithFilters(map[string]any{
			"source": map[string]any{"$ne": fn.File},
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to search similar code: %w", err)
	}

	if len(docs) == 0 {
		return nil, nil
	}

	// 3. Verification
	for _, doc := range docs {
		existingCode := doc.PageContent
		existingFile, _ := doc.Metadata["source"].(string)

		isMatch, confidence, err := d.verifyMatch(ctx, fn.Code, existingCode)
		if err != nil {
			continue // Try the next doc
		}

		if isMatch && confidence >= 0.8 { // Threshold for confidence
			return &ReuseSuggestion{
				FilePath: fn.File,
				Line:     fn.Line,
				Message:  fmt.Sprintf("Consider using `%s` instead. This code appears to duplicate existing functionality with high confidence (%.2f).", existingFile, confidence),
			}, nil
		}
	}

	return nil, nil
}

func (d *reuseDetector) extractIntent(ctx context.Context, code string) (string, error) {
	prompt := fmt.Sprintf(`Analyze this code. Write a short, natural language sentence describing *what* it does, not *how* it does it. Example: 'Validates an email address regex'.

Code:
%s

Intent Sentence:`, code)

	return generateText(ctx, d.llmModel, prompt)
}

func (d *reuseDetector) verifyMatch(ctx context.Context, newCode, existingCode string) (bool, float64, error) {
	prompt := fmt.Sprintf(`Does the existing code "B" provide the same functionality as the new code "A"? If yes, should the user refactor to use "B"? 
Output your answer as a JSON object with two fields: "is_match" (boolean) and "confidence" (number between 0 and 1). Make sure the output is exactly valid JSON and nothing else.

New Code A:
%s

Existing Code B:
%s

JSON Output:`, newCode, existingCode)

	resp, err := generateText(ctx, d.llmModel, prompt)
	if err != nil {
		return false, 0, err
	}

	// Extremely naive JSON parsing for the sake of strict go code limits.
	// In a real scenario we might use a dedicated structured output parser or strict JSON unmarshaling.
	isMatch := strings.Contains(strings.ToLower(resp), `"is_match": true`) || strings.Contains(strings.ToLower(resp), `"is_match":true`)

	// Extract confidence using regex to find the number pattern
	re := regexp.MustCompile(`"confidence"\s*:\s*([0-9.]+)`)
	matches := re.FindStringSubmatch(resp)

	confidence := 0.0
	if len(matches) > 1 {
		if val, err := strconv.ParseFloat(matches[1], 64); err == nil {
			confidence = val
		}
	}

	return isMatch, confidence, nil
}

// Helper to generate text from a string prompt easily.
func generateText(ctx context.Context, llmModel llms.Model, prompt string) (string, error) {
	return llms.GenerateFromSinglePrompt(ctx, llmModel, prompt)
}

// extractAddedFunctions uses naive diff parsing to find newly added functions.
func (d *reuseDetector) extractAddedFunctions(changedFiles []internalgithub.ChangedFile) []addedFunction {
	var results []addedFunction
	for _, cf := range changedFiles {
		if !strings.HasSuffix(cf.Filename, ".go") {
			continue // Focus on Go files for now to identify functions
		}

		lines := strings.Split(cf.Patch, "\n")
		var currentLineNumber int
		var inFunction bool
		var functionLines []string
		var funcStartLine int

		// Hunk format looks roughly like @@ -10,5 +10,12 @@
		// We can parse the +10 part to know the exact line number
		hunkHeaderRegex := regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

		for _, line := range lines {
			if strings.HasPrefix(line, "@@ ") {
				matches := hunkHeaderRegex.FindStringSubmatch(line)
				if len(matches) > 1 {
					currentLineNumber, _ = strconv.Atoi(matches[1])
					currentLineNumber-- // Offset by 1 because the first iteration will increment it
				}
				inFunction = false
				continue
			}

			if !strings.HasPrefix(line, "-") {
				currentLineNumber++
			}

			isAddedLine := strings.HasPrefix(line, "+")
			cleanLine := line
			if isAddedLine || strings.HasPrefix(line, " ") {
				cleanLine = line[1:] // strip + or space
			}

			if isAddedLine && (strings.HasPrefix(strings.TrimSpace(cleanLine), "func ") || strings.HasPrefix(strings.TrimSpace(cleanLine), "func(")) {
				// End previous function if any
				if inFunction {
					results = append(results, addedFunction{
						File: cf.Filename,
						Line: funcStartLine,
						Code: strings.Join(functionLines, "\n"),
					})
				}
				inFunction = true
				funcStartLine = currentLineNumber
				functionLines = []string{cleanLine}
			} else if inFunction {
				functionLines = append(functionLines, cleanLine)
				// Basic heuristic to stop collecting: empty line or end of func (often just "}")
				if strings.TrimSpace(cleanLine) == "}" {
					results = append(results, addedFunction{
						File: cf.Filename,
						Line: funcStartLine,
						Code: strings.Join(functionLines, "\n"),
					})
					inFunction = false
				}
			}
		}

		if inFunction {
			results = append(results, addedFunction{
				File: cf.Filename,
				Line: funcStartLine,
				Code: strings.Join(functionLines, "\n"),
			})
		}
	}
	return results
}
