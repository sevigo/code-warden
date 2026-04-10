package agent

import (
	"context"
	"sync/atomic"

	"github.com/sevigo/goframe/llms"
	"github.com/sevigo/goframe/schema"
)

// TracingModel wraps an llms.Model and accumulates token usage from every
// GenerateContent call.  The Ollama (and Gemini) providers populate
// GenerationInfo with PromptTokens / CompletionTokens on each response, but
// goframe's AgentLoop never extracts them — LoopResult.Tokens is always zero.
//
// TracingModel fixes this by reading those values after each call and
// accumulating them in atomic counters.  After the loop completes, callers
// can read the totals with TokenCounts().
type TracingModel struct {
	inner     llms.Model
	tokensIn  atomic.Int64
	tokensOut atomic.Int64
	calls     atomic.Int64
}

// NewTracingModel creates a TracingModel that delegates to inner.
func NewTracingModel(inner llms.Model) *TracingModel {
	return &TracingModel{inner: inner}
}

// GenerateContent delegates to the wrapped model and accumulates token counts
// from the response's GenerationInfo.
func (t *TracingModel) GenerateContent(ctx context.Context, messages []schema.MessageContent, options ...llms.CallOption) (*schema.ContentResponse, error) {
	resp, err := t.inner.GenerateContent(ctx, messages, options...)
	if err != nil {
		return nil, err
	}

	t.calls.Add(1)

	for _, choice := range resp.Choices {
		if choice.GenerationInfo == nil {
			continue
		}
		if v, ok := choice.GenerationInfo["PromptTokens"]; ok {
			if f, ok := v.(float64); ok && f > 0 {
				t.tokensIn.Add(int64(f))
			}
		}
		if v, ok := choice.GenerationInfo["CompletionTokens"]; ok {
			if f, ok := v.(float64); ok && f > 0 {
				t.tokensOut.Add(int64(f))
			}
		}
	}

	return resp, nil
}

// Call delegates to the wrapped model.  Token counts are not available for
// single-turn Call invocations (they return plain strings), so this only
// increments the call counter.
func (t *TracingModel) Call(ctx context.Context, prompt string, options ...llms.CallOption) (string, error) {
	t.calls.Add(1)
	return t.inner.Call(ctx, prompt, options...)
}

// TokenCounts returns the accumulated input tokens, output tokens, and total
// LLM call count since the last Reset.
func (t *TracingModel) TokenCounts() (tokensIn, tokensOut, calls int64) {
	return t.tokensIn.Load(), t.tokensOut.Load(), t.calls.Load()
}

// Reset zeroes all accumulated counters.
func (t *TracingModel) Reset() {
	t.tokensIn.Store(0)
	t.tokensOut.Store(0)
	t.calls.Store(0)
}
