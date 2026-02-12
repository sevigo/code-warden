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
