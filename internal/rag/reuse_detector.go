package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"

	"github.com/sevigo/goframe/llms"
	"github.com/sevigo/goframe/schema"
	"github.com/sevigo/goframe/vectorstores"
	"golang.org/x/sync/errgroup"

	internalgithub "github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/llm"
	"github.com/sevigo/code-warden/internal/storage"
)

// minFunctionLines is the minimum number of added lines for a function to be considered
// "significant" enough to check for reuse. This filters out trivial one-liners.
const minFunctionLines = 4

// judgeConfidenceThreshold is the minimum confidence score from the judge LLM
// to consider an existing function a duplicate.
const judgeConfidenceThreshold = 0.7

// maxConcurrentDetections limits the number of parallel intent+search+judge goroutines.
const maxConcurrentDetections = 5

// newFuncRegex matches newly added Go function definitions in a unified diff patch.
// It captures the function name. Supports both standalone functions and methods with receivers.
var newFuncRegex = regexp.MustCompile(`^\+func\s+(?:\([^)]+\)\s+)?(\w+)\(`)

// ReuseSuggestion represents a detected case where new code may duplicate existing functionality.
type ReuseSuggestion struct {
	FilePath string // File containing the new (potentially redundant) function
	Line     int    // Approximate line number within the diff
	Message  string // Human-readable suggestion message
}

// newFunction represents a newly added function extracted from a diff patch.
type newFunction struct {
	Name     string // Function name
	Body     string // Cleaned function body (diff markers removed)
	FilePath string // File where the function was added
	Line     int    // Line number in the diff where the function starts
}

// judgeResult is the parsed JSON output from the reuse judge LLM.
type judgeResult struct {
	Duplicate  bool    `json:"duplicate"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}

// ReuseDetector analyzes changed files to detect potential code reuse opportunities
// by comparing newly added functions against the existing codebase via semantic search.
type ReuseDetector interface {
	Detect(ctx context.Context, changedFiles []internalgithub.ChangedFile, scopedStore storage.ScopedVectorStore) ([]ReuseSuggestion, error)
}

type reuseDetector struct {
	model     llms.Model
	promptMgr *llm.PromptManager
	logger    *slog.Logger
}

// NewReuseDetector creates a new ReuseDetector that uses the given LLM model
// for intent extraction and duplicate verification.
func NewReuseDetector(model llms.Model, promptMgr *llm.PromptManager, logger *slog.Logger) ReuseDetector {
	return &reuseDetector{
		model:     model,
		promptMgr: promptMgr,
		logger:    logger,
	}
}

// Detect scans the changed files for newly added functions, extracts their intent,
// searches the vector store for similar existing code, and returns suggestions
// for functions that appear to duplicate existing functionality.
func (d *reuseDetector) Detect(ctx context.Context, changedFiles []internalgithub.ChangedFile, scopedStore storage.ScopedVectorStore) ([]ReuseSuggestion, error) {
	// Step 1: Parse all changed files to find new functions
	var allFunctions []newFunction
	for _, file := range changedFiles {
		if file.Patch == "" {
			continue
		}
		funcs := parseNewFunctions(file.Filename, file.Patch)
		allFunctions = append(allFunctions, funcs...)
	}

	if len(allFunctions) == 0 {
		d.logger.Info("reuse detector: no significant new functions found in diff")
		return nil, nil
	}

	d.logger.Info("reuse detector: new functions found",
		"count", len(allFunctions),
	)

	// Step 2: Process each function in parallel using errgroup
	var (
		mu          sync.Mutex
		suggestions []ReuseSuggestion
	)

	g, gCtx := errgroup.WithContext(ctx)
	sem := make(chan struct{}, maxConcurrentDetections)

	for _, fn := range allFunctions {
		g.Go(func() error {
			// Acquire semaphore
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-gCtx.Done():
				return gCtx.Err()
			}

			results, err := d.processFunction(gCtx, fn, scopedStore)
			if err != nil {
				d.logger.Warn("reuse detector: failed to process function",
					"function", fn.Name,
					"file", fn.FilePath,
					"error", err,
				)
				// Non-fatal: continue processing other functions
				return nil
			}

			if len(results) > 0 {
				mu.Lock()
				suggestions = append(suggestions, results...)
				mu.Unlock()
			}

			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return suggestions, fmt.Errorf("reuse detection failed: %w", err)
	}

	d.logger.Info("reuse detector: analysis complete",
		"suggestions", len(suggestions),
	)

	return suggestions, nil
}

// processFunction handles the full intent→retrieve→judge pipeline for a single function.
func (d *reuseDetector) processFunction(ctx context.Context, fn newFunction, scopedStore storage.ScopedVectorStore) ([]ReuseSuggestion, error) {
	// Step 2a: Intent Extraction - generate a semantic query from the function
	intent, err := d.extractIntent(ctx, fn.Body)
	if err != nil {
		return nil, fmt.Errorf("intent extraction failed for %s: %w", fn.Name, err)
	}

	d.logger.Debug("reuse detector: intent extracted",
		"function", fn.Name,
		"intent", intent,
	)

	// Step 2b: Retrieval - search for similar existing code, excluding the current file
	docs, err := scopedStore.SimilaritySearch(ctx, intent, 3,
		vectorstores.WithFilters(map[string]any{
			"source": map[string]any{
				"$ne": fn.FilePath,
			},
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("similarity search failed for %s: %w", fn.Name, err)
	}

	if len(docs) == 0 {
		return nil, nil
	}

	// Step 2c: Verification - judge each candidate
	var suggestions []ReuseSuggestion
	for _, doc := range docs {
		suggestion, err := d.judgeCandidate(ctx, fn, doc)
		if err != nil {
			d.logger.Debug("reuse detector: judge failed for candidate",
				"function", fn.Name,
				"candidate_source", doc.Metadata["source"],
				"error", err,
			)
			continue
		}
		if suggestion != nil {
			suggestions = append(suggestions, *suggestion)
		}
	}

	return suggestions, nil
}

// extractIntent uses the LLM to generate a short intent description for the given code.
func (d *reuseDetector) extractIntent(ctx context.Context, code string) (string, error) {
	prompt, err := d.promptMgr.Render(llm.ReuseIntentPrompt, map[string]string{
		"Code": code,
	})
	if err != nil {
		return "", fmt.Errorf("failed to render intent prompt: %w", err)
	}

	response, err := d.model.Call(ctx, prompt)
	if err != nil {
		return "", fmt.Errorf("LLM call failed: %w", err)
	}

	// Clean up the response - take only the first line and trim whitespace
	intent := strings.TrimSpace(response)
	if idx := strings.Index(intent, "\n"); idx > 0 {
		intent = intent[:idx]
	}

	return intent, nil
}

// judgeCandidate asks the LLM to compare the new function against an existing candidate.
// Returns a ReuseSuggestion if the judge determines they are duplicates with high confidence.
func (d *reuseDetector) judgeCandidate(ctx context.Context, fn newFunction, candidate schema.Document) (*ReuseSuggestion, error) {
	existingFile, _ := candidate.Metadata["source"].(string)
	if existingFile == "" {
		existingFile = "unknown"
	}

	prompt, err := d.promptMgr.Render(llm.ReuseJudgePrompt, map[string]string{
		"NewCode":      fn.Body,
		"ExistingFile": existingFile,
		"ExistingCode": candidate.PageContent,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to render judge prompt: %w", err)
	}

	response, err := d.model.Call(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("LLM judge call failed: %w", err)
	}

	result, err := parseJudgeResult(response)
	if err != nil {
		return nil, fmt.Errorf("failed to parse judge response: %w", err)
	}

	if !result.Duplicate || result.Confidence < judgeConfidenceThreshold {
		return nil, nil
	}

	// Extract identifier from existing code if available
	identifier, _ := candidate.Metadata["identifier"].(string)
	var message string
	if identifier != "" {
		message = fmt.Sprintf("Consider using existing `%s` from `%s` instead of creating `%s`. %s (confidence: %.0f%%)",
			identifier, existingFile, fn.Name, result.Reason, result.Confidence*100)
	} else {
		message = fmt.Sprintf("Similar functionality already exists in `%s`. Consider reusing it instead of creating `%s`. %s (confidence: %.0f%%)",
			existingFile, fn.Name, result.Reason, result.Confidence*100)
	}

	return &ReuseSuggestion{
		FilePath: fn.FilePath,
		Line:     fn.Line,
		Message:  message,
	}, nil
}

// parseJudgeResult extracts a judgeResult from the LLM's JSON response.
func parseJudgeResult(response string) (*judgeResult, error) {
	// Try to find JSON in the response
	start := strings.Index(response, "{")
	end := strings.LastIndex(response, "}")
	if start == -1 || end == -1 || end < start {
		return nil, fmt.Errorf("no JSON found in judge response")
	}

	jsonStr := response[start : end+1]
	var result judgeResult
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal judge JSON: %w", err)
	}

	return &result, nil
}

// parseNewFunctions scans a unified diff patch and extracts newly added function definitions.
// It skips trivial functions with fewer than minFunctionLines added lines.
func parseNewFunctions(filePath, patch string) []newFunction {
	lines := strings.Split(patch, "\n")
	var functions []newFunction

	var (
		currentFunc    *newFunction
		bodyLines      []string
		addedLineCount int
	)

	for lineNum, line := range lines {
		// Check if this line starts a new function definition
		if matches := newFuncRegex.FindStringSubmatch(line); matches != nil {
			// If we were tracking a previous function, finalize it
			if currentFunc != nil {
				finalizeFunction(&functions, currentFunc, bodyLines, addedLineCount)
			}

			// Start tracking the new function
			cleanLine := strings.TrimPrefix(line, "+")
			currentFunc = &newFunction{
				Name:     matches[1],
				FilePath: filePath,
				Line:     lineNum + 1, // 1-indexed
			}
			bodyLines = []string{cleanLine}
			addedLineCount = 1
			continue
		}

		// If we're inside a function, collect its body
		if currentFunc != nil {
			if strings.HasPrefix(line, "+") {
				cleanLine := strings.TrimPrefix(line, "+")
				bodyLines = append(bodyLines, cleanLine)
				addedLineCount++
			} else if strings.HasPrefix(line, "-") {
				// Deleted lines in a diff — skip, don't count as part of new function
				continue
			} else if strings.HasPrefix(line, "@@") {
				// New hunk header — finalize current function and stop tracking
				finalizeFunction(&functions, currentFunc, bodyLines, addedLineCount)
				currentFunc = nil
				bodyLines = nil
				addedLineCount = 0
			} else {
				// Context line (unchanged) — still part of the function body for understanding
				bodyLines = append(bodyLines, line)
			}
		}
	}

	// Finalize the last function if we were still tracking one
	if currentFunc != nil {
		finalizeFunction(&functions, currentFunc, bodyLines, addedLineCount)
	}

	return functions
}

// finalizeFunction adds a function to the result list if it meets the minimum line threshold.
func finalizeFunction(functions *[]newFunction, fn *newFunction, bodyLines []string, addedLineCount int) {
	if addedLineCount >= minFunctionLines {
		fn.Body = strings.Join(bodyLines, "\n")
		*functions = append(*functions, *fn)
	}
}
