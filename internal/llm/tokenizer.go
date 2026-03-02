package llm

import (
	"context"

	"github.com/sevigo/goframe/llms"
	"github.com/sevigo/goframe/textsplitter"
)

// OllamaTokenizerAdapter adapts the GoFrame Ollama LLM to the Tokenizer interface.
type OllamaTokenizerAdapter struct {
	model llms.Model
}

// NewOllamaTokenizerAdapter creates a new adapter for Ollama tokenization.
func NewOllamaTokenizerAdapter(model llms.Model) textsplitter.Tokenizer {
	return &OllamaTokenizerAdapter{
		model: model,
	}
}

// CountTokens returns the number of tokens in the given text using the Ollama model.
func (a *OllamaTokenizerAdapter) CountTokens(ctx context.Context, _, text string) int {
	if t, ok := a.model.(llms.Tokenizer); ok {
		n, err := t.CountTokens(ctx, text)
		if err != nil {
			return a.EstimateTokens(ctx, "", text)
		}
		return n
	}
	return a.EstimateTokens(ctx, "", text)
}

// EstimateTokens provides a fast, character-based estimation of token count.
func (a *OllamaTokenizerAdapter) EstimateTokens(_ context.Context, _, text string) int {
	return len(text) / 3
}

// SplitTextByTokens splits text by token count (fallback to character-based splitting).
func (a *OllamaTokenizerAdapter) SplitTextByTokens(_ context.Context, _, text string, maxTokens int) ([]string, error) {
	maxChars := maxTokens * 3
	var chunks []string
	for len(text) > maxChars {
		chunks = append(chunks, text[:maxChars])
		text = text[maxChars:]
	}
	if len(text) > 0 {
		chunks = append(chunks, text)
	}
	return chunks, nil
}

// GetRecommendedChunkSize returns the recommended chunk size in characters.
func (a *OllamaTokenizerAdapter) GetRecommendedChunkSize(_ context.Context, _ string) int {
	return 2000
}

// GetOptimalOverlapTokens returns the optimal overlap in tokens.
func (a *OllamaTokenizerAdapter) GetOptimalOverlapTokens(_ context.Context, _ string) int {
	return 50
}

// GetMaxContextWindow returns the maximum context window for the model.
func (a *OllamaTokenizerAdapter) GetMaxContextWindow(_ context.Context, _ string) int {
	return 8192
}

// EstimatingTokenizer is a fallback tokenizer that uses character-based estimation.
// It's used when the LLM provider doesn't implement llms.Tokenizer (e.g., Gemini).
type EstimatingTokenizer struct{}

// NewEstimatingTokenizer creates a tokenizer that estimates token count from characters.
func NewEstimatingTokenizer() llms.Tokenizer {
	return &EstimatingTokenizer{}
}

// CountTokens estimates token count using the industry-standard approximation
// of 1 token ≈ 3-4 characters for English text.
// Note: This is less accurate for code or non-English languages.
func (e *EstimatingTokenizer) CountTokens(_ context.Context, text string) (int, error) {
	return len(text) / 3, nil
}

// SafeTokenizer wraps an llms.Tokenizer and provides a fallback to estimation
// if the primary tokenizer fails (e.g., when the provider rejects token counting calls).
type SafeTokenizer struct {
	base llms.Tokenizer
}

// CountTokens calls the base tokenizer but falls back to estimation on error.
func (t *SafeTokenizer) CountTokens(ctx context.Context, text string) (int, error) {
	if n, err := t.base.CountTokens(ctx, text); err == nil {
		return n, nil
	}
	// Fallback to estimation if primary tokenizer fails
	return len(text) / 3, nil
}

// AsTokenizer returns an llms.Tokenizer for the given model.
// If the model implements llms.Tokenizer, it's returned wrapped in a SafeTokenizer.
// Otherwise, an EstimatingTokenizer is returned.
func AsTokenizer(model llms.Model) llms.Tokenizer {
	if t, ok := model.(llms.Tokenizer); ok {
		return &SafeTokenizer{base: t}
	}
	return NewEstimatingTokenizer()
}
