package detect

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"

	"github.com/sevigo/goframe/chains"
	"github.com/sevigo/goframe/llms"
	"github.com/sevigo/goframe/output"
	"github.com/sevigo/goframe/prompts"
	"github.com/sevigo/goframe/schema"
	"github.com/sevigo/goframe/vectorstores"
	"golang.org/x/sync/errgroup"

	internalgithub "github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/llm"
	"github.com/sevigo/code-warden/internal/storage"
)

// ReuseSuggestion represents a detected code redundancy with confidence score.
type ReuseSuggestion struct {
	FilePath     string  `json:"file_path"`
	LineNumber   int     `json:"line_number"`
	Message      string  `json:"message"`
	Confidence   float64 `json:"confidence"`
	ExistingFile string  `json:"existing_file"`
	ExistingCode string  `json:"existing_code,omitempty"`
}

// Function represents an extracted function from a code diff.
type extractedFunction struct {
	Name     string
	Content  string
	FilePath string
	LineNum  int // Approximate line number in the file
}

// ReuseDetector detects code redundancies using intent-match similarity search.
type ReuseDetector struct {
	llm         llms.Model
	promptMgr   *llm.PromptManager
	vectorStore storage.VectorStore
	logger      *slog.Logger

	// Regex patterns for function extraction
	funcPattern *regexp.Regexp
}

// verificationResult holds the LLM's verdict on whether code is redundant.
type verificationResult struct {
	IsRedundant bool    `json:"is_redundant"`
	Confidence  float64 `json:"confidence"`
	Reason      string  `json:"reason"`
	Suggestion  string  `json:"suggestion"`
}

// NewReuseDetector creates a new ReuseDetector instance.
func NewReuseDetector(
	llm llms.Model,
	promptMgr *llm.PromptManager,
	vectorStore storage.VectorStore,
	logger *slog.Logger,
) *ReuseDetector {
	return &ReuseDetector{
		llm:         llm,
		promptMgr:   promptMgr,
		vectorStore: vectorStore,
		logger:      logger,
		// Pattern matches: func Name( or func (receiver) Name(
		funcPattern: regexp.MustCompile(`(?m)^\+?\s*func\s+(?:\([^)]+\)\s*)?(\w+)\s*\(`),
	}
}

// DetectRedundancies analyzes changed files and returns reuse suggestions.
func (d *ReuseDetector) DetectRedundancies(
	ctx context.Context,
	collectionName string,
	embedderModel string,
	changedFiles []internalgithub.ChangedFile,
) ([]ReuseSuggestion, error) {
	// Extract new functions from all changed files
	var newFunctions []extractedFunction
	for _, file := range changedFiles {
		funcs := d.extractNewFunctions(file)
		newFunctions = append(newFunctions, funcs...)
	}

	if len(newFunctions) == 0 {
		d.logger.Info("no new functions found in diff")
		return nil, nil
	}

	d.logger.Info("detecting redundancies", "new_functions", len(newFunctions))

	// Process functions concurrently using errgroup
	var mu sync.Mutex
	var suggestions []ReuseSuggestion

	g, ctx := errgroup.WithContext(ctx)
	// Limit concurrency to avoid overwhelming the vector store and LLM
	g.SetLimit(5)

	for _, fn := range newFunctions {
		g.Go(func() error {
			suggestion, err := d.processFunction(ctx, collectionName, embedderModel, fn)
			if err != nil {
				d.logger.Warn("failed to process function for redundancy detection",
					"function", fn.Name,
					"file", fn.FilePath,
					"error", err,
				)
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

	if err := g.Wait(); err != nil {
		return nil, fmt.Errorf("redundancy detection failed: %w", err)
	}

	d.logger.Info("redundancy detection complete", "suggestions_found", len(suggestions))
	return suggestions, nil
}

// extractNewFunctions extracts function definitions from added lines in a patch.
func (d *ReuseDetector) extractNewFunctions(file internalgithub.ChangedFile) []extractedFunction {
	if file.Patch == "" {
		return nil
	}

	var functions []extractedFunction
	lines := strings.Split(file.Patch, "\n")
	lineNum := 0

	for i, line := range lines {
		// Track approximate line number from patch
		if strings.HasPrefix(line, "@@") {
			// Parse hunk header like "@@ -1,5 +10,7 @@"
			lineNum = parseHunkStartLine(line)
			continue
		}

		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			lineNum++
		} else if !strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "@@") {
			lineNum++
		}

		matches := d.funcPattern.FindStringSubmatch(line)
		if len(matches) < 2 {
			continue
		}

		funcName := matches[1]

		// Skip small functions (likely one-liners or getters/setters)
		funcBody := d.extractFunctionBody(lines, i)
		if len(funcBody) < 3 { // At least 3 lines to be significant
			if d.logger != nil {
				d.logger.Debug("skipping small function", "name", funcName, "lines", len(funcBody))
			}
			continue
		}

		functions = append(functions, extractedFunction{
			Name:     funcName,
			Content:  strings.Join(funcBody, "\n"),
			FilePath: file.Filename,
			LineNum:  lineNum,
		})
	}

	return functions
}

// extractFunctionBody extracts lines of a function body by matching braces.
func (d *ReuseDetector) extractFunctionBody(lines []string, startIdx int) []string {
	var body []string
	braceCount := 0
	foundOpeningBrace := false

	for i := startIdx; i < len(lines); i++ {
		line := lines[i]

		// Only include added lines or context lines within the function
		if strings.HasPrefix(line, "+") || strings.HasPrefix(line, " ") {
			body = append(body, line)
		}

		// Count braces to find function boundaries
		for _, ch := range line {
			switch ch {
			case '{':
				braceCount++
				foundOpeningBrace = true
			case '}':
				braceCount--
			}
		}

		// Function ends when braces balance and we've seen an opening brace
		if foundOpeningBrace && braceCount == 0 {
			break
		}
	}

	return body
}

// processFunction runs intent extraction, retrieval, and verification for one function.
func (d *ReuseDetector) processFunction(
	ctx context.Context,
	collectionName string,
	embedderModel string,
	fn extractedFunction,
) (*ReuseSuggestion, error) {
	// Step 1: Extract intent using LLM
	intentQuery, err := d.extractIntent(ctx, fn)
	if err != nil {
		return nil, fmt.Errorf("intent extraction failed: %w", err)
	}

	d.logger.Debug("extracted intent for function",
		"function", fn.Name,
		"intent", intentQuery,
	)

	// Step 2: Retrieve similar code from vector store, excluding current file
	candidates, err := d.retrieveSimilarCode(ctx, collectionName, embedderModel, intentQuery, fn.FilePath)
	if err != nil {
		return nil, fmt.Errorf("retrieval failed: %w", err)
	}

	if len(candidates) == 0 {
		return nil, nil
	}

	// Step 3: Verify each candidate against the new function
	return d.verifyRedundancy(ctx, fn, candidates)
}

// extractIntent uses an LLM to generate a semantic description of a function's purpose.
func (d *ReuseDetector) extractIntent(ctx context.Context, fn extractedFunction) (string, error) {
	promptData := map[string]string{
		"Code": fn.Content,
	}

	prompt, err := d.promptMgr.Render(llm.IntentExtractionPrompt, promptData)
	if err != nil {
		return "", fmt.Errorf("failed to render intent prompt: %w", err)
	}

	chain, err := chains.NewLLMChain[string](
		d.llm,
		prompts.NewPromptTemplate(prompt),
	)
	if err != nil {
		return "", fmt.Errorf("failed to create LLM chain: %w", err)
	}

	result, err := chain.Call(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("LLM call failed: %w", err)
	}

	return result, nil
}

// retrieveSimilarCode searches the vector store for similar code, excluding the source file.
func (d *ReuseDetector) retrieveSimilarCode(
	ctx context.Context,
	collectionName string,
	embedderModel string,
	query string,
	excludeFile string,
) ([]schema.Document, error) {
	scopedStore := d.vectorStore.ForRepo(collectionName, embedderModel)

	// Use WithFilter to exclude the current file from results
	// The filter excludes documents where "source" equals the current file path
	results, err := scopedStore.SimilaritySearch(
		ctx,
		query,
		3,
		vectorstores.WithFilter("source", excludeFile),
	)
	if err != nil {
		return nil, fmt.Errorf("similarity search failed: %w", err)
	}

	return results, nil
}

// verifyRedundancy uses an LLM to compare a new function against retrieved candidates.
func (d *ReuseDetector) verifyRedundancy(
	ctx context.Context,
	newFunc extractedFunction,
	candidates []schema.Document,
) (*ReuseSuggestion, error) {
	parser := output.NewJSONParser[verificationResult]()

	for _, candidate := range candidates {
		// Skip if same file (shouldn't happen due to filter, but be safe)
		candidateSource, _ := candidate.Metadata["source"].(string)
		if candidateSource == newFunc.FilePath {
			continue
		}

		promptData := map[string]string{
			"NewCode":      newFunc.Content,
			"ExistingCode": candidate.PageContent,
			"ExistingPath": candidateSource,
		}

		prompt, err := d.promptMgr.Render(llm.ReuseVerificationPrompt, promptData)
		if err != nil {
			d.logger.Warn("failed to render verification prompt", "error", err)
			continue
		}

		chain, err := chains.NewLLMChain(
			d.llm,
			prompts.NewPromptTemplate(prompt),
			chains.WithOutputParser(parser),
		)
		if err != nil {
			d.logger.Warn("failed to create LLM chain", "error", err)
			continue
		}

		verdict, err := chain.Call(ctx, nil)
		if err != nil {
			d.logger.Warn("verification LLM call failed", "error", err)
			continue
		}

		// Only return suggestions with high confidence
		if verdict.IsRedundant && verdict.Confidence >= 0.7 {
			return &ReuseSuggestion{
				FilePath:     newFunc.FilePath,
				LineNumber:   newFunc.LineNum,
				Message:      verdict.Suggestion,
				Confidence:   verdict.Confidence,
				ExistingFile: candidateSource,
				ExistingCode: candidate.PageContent,
			}, nil
		}
	}

	return nil, nil
}

// parseHunkStartLine extracts the new-file start line number from a hunk header.
func parseHunkStartLine(hunkHeader string) int {
	// Format: @@ -oldStart,oldCount +newStart,newCount @@
	// Example: @@ -1,5 +10,7 @@
	parts := strings.Split(hunkHeader, " ")
	for _, part := range parts {
		if strings.HasPrefix(part, "+") {
			// Remove the + and any comma/count
			lineInfo := strings.TrimPrefix(part, "+")
			lineInfo = strings.Split(lineInfo, ",")[0]
			var lineNum int
			if _, err := fmt.Sscanf(lineInfo, "%d", &lineNum); err == nil {
				return lineNum
			}
		}
	}
	return 0
}
