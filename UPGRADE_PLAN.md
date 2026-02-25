# goframe v0.26.0 Upgrade Plan for code-warden

## Current State
- **goframe version**: v0.25.1 → v0.26.0 (upgraded)
- **ollama version**: v0.15.4 → v0.17.0 (upgraded)

## New Features Available

### 1. Thinking/Reasoning Mode 🧠
**Impact: HIGH** - Especially for code reviews with reasoning models

**Use Case**: Enable deep reasoning for code reviews using models like DeepSeek-R1, Qwen 3, etc.

**Files to modify**:
- `internal/config/config.go` - Add `EnableThinking`, `ThinkingEffort` config
- `internal/wire/wire.go` - Pass thinking options to Ollama
- `internal/rag/rag_review.go` - Extract and display thinking traces

**Implementation**:
```go
// config.go
type AIConfig struct {
    // ... existing fields
    EnableThinking   bool   `mapstructure:"enable_thinking"`
    ThinkingEffort   string `mapstructure:"thinking_effort"` // "low", "medium", "high"
}

// wire.go
ollama.WithThinking(cfg.AI.EnableThinking),
ollama.WithReasoningEffort(cfg.AI.ThinkingEffort),

// rag_review.go - Extract thinking from response
if thinking, ok := response.Choices[0].GenerationInfo["Thinking"].(string); ok {
    r.logger.Debug("model thinking trace", "thinking", thinking)
}
```

**Benefits**:
- Higher quality code reviews with reasoning models
- Transparent decision-making process
- Better detection of subtle bugs and security issues

---

### 2. Structured Outputs (JSON Mode) 📋
**Impact: MEDIUM** - Could simplify structured review parsing

**Use Case**: Guarantee JSON output from LLM, making `StructuredReview` parsing more reliable.

**Files to modify**:
- `internal/rag/rag_review.go` - Use JSON mode for structured output
- `internal/llm/prompts.go` - Add JSON response format to prompts

**Implementation**:
```go
// rag_review.go - In GenerateReview
response, err := chain.Call(ctx, nil, llms.WithJSONMode(true))
// or with schema
response, err := chain.Call(ctx, nil, llms.WithJSONSchema(reviewSchema))
```

**Benefits**:
- Eliminate markdown parsing failures
- Guaranteed structured output
- Simpler error handling

---

### 3. Tool/Function Calling 🛠️
**Impact: MEDIUM** - Enable interactive code analysis

**Use Case**: Allow the review model to request additional context (file contents, type definitions, etc.)

**Potential Tools**:
```go
tools := []llms.ToolDefinition{
    {
        Type: "function",
        Function: llms.FunctionDefinition{
            Name:        "get_file_content",
            Description: "Get the full content of a file from the repository",
            Parameters: map[string]any{
                "type": "object",
                "properties": map[string]any{
                    "filepath": map[string]any{"type": "string"},
                },
                "required": []string{"filepath"},
            },
        },
    },
    {
        Type: "function",
        Function: llms.FunctionDefinition{
            Name:        "search_code",
            Description: "Search for code patterns across the repository",
            Parameters: map[string]any{
                "type": "object",
                "properties": map[string]any{
                    "query": map[string]any{"type": "string"},
                    "limit": map[string]any{"type": "integer"},
                },
                "required": []string{"query"},
            },
        },
    },
}
```

**Benefits**:
- Model can request additional context dynamically
- More precise reviews
- Better understanding of cross-file dependencies

---

### 4. Embedding Options (Truncate, Dimensions) 📐
**Impact: MEDIUM** - Better control over embeddings

**Use Case**: Reduce embedding dimensions for faster search, or truncate large files.

**Files to modify**:
- `internal/config/config.go` - Add embedding dimensions config
- `internal/storage/vectorstore.go` - Use `EmbedDocumentsWithOpts`
- `internal/wire/wire.go` - Pass embedding options

**Implementation**:
```go
// config.go
type AIConfig struct {
    // ... existing fields
    EmbedderDimensions int  `mapstructure:"embedder_dimensions"` // 0 = full
    EmbedderTruncate   bool `mapstructure:"embedder_truncate"`
}

// vectorstore.go
embeddings, err := e.client.EmbedDocumentsWithOpts(ctx, texts, embeddings.EmbeddingOptions{
    Truncate:   e.cfg.AI.EmbedderTruncate,
    Dimensions: e.cfg.AI.EmbedderDimensions,
})
```

**Benefits**:
- Smaller embeddings = faster search
- Control memory usage
- Handle large files gracefully

---

### 5. Model Management 📦
**Impact: LOW** - Operational improvements

**New methods available**:
- `ListModels()` - Check available models
- `ListRunningModels()` - See loaded models
- `DeleteModel()` - Cleanup old models
- `CopyModel()` - Create aliases
- `GetVersion()` - Server version

**Use Case**: Health checks, model pre-loading, cleanup.

**Implementation**:
```go
// Add health check endpoint
func (s *Service) CheckAIHealth(ctx context.Context) (*AIHealth, error) {
    running, err := s.llm.ListRunningModels(ctx)
    if err != nil {
        return nil, err
    }
    version, _ := s.llm.GetVersion(ctx)
    return &AIHealth{
        ServerVersion: version,
        RunningModels: running,
    }, nil
}
```

---

### 6. MinP Sampling Parameter 🎯
**Impact: LOW** - Better generation control

**Use Case**: Reduce low-quality tokens in generation.

**Implementation**:
```go
// In CallOptions
llms.WithMinP(0.05), // Filter tokens with probability < 5%
```

---

### 7. KeepAlive for Model Memory 💾
**Impact: MEDIUM** - Performance optimization

**Use Case**: Keep models loaded between reviews for faster response.

**Files to modify**:
- `internal/config/config.go` - Add `ModelKeepAlive` config
- `internal/wire/wire.go` - Pass keep_alive option

**Implementation**:
```go
// config.go
type AIConfig struct {
    // ... existing fields
    ModelKeepAlive string `mapstructure:"model_keep_alive"` // e.g., "10m", "1h"
}

// wire.go - In provideGeneratorLLM
ollama.WithKeepAlive(cfg.AI.ModelKeepAlive),
```

**Benefits**:
- Faster subsequent reviews
- Reduced cold-start latency
- Better resource management

---

## Implementation Priority

### Phase 1: High Impact (Week 1)
1. ✅ **Thinking/Reasoning Mode** - Most impactful for code review quality
2. ✅ **KeepAlive** - Immediate performance improvement

### Phase 2: Medium Impact (Week 2)
3. **Embedding Options** - Memory and speed optimization
4. **Structured Outputs** - More reliable parsing

### Phase 3: Future Enhancements (Week 3+)
5. **Tool Calling** - Interactive context retrieval
6. **Model Management** - Health checks and operations

---

## Configuration Changes

Add to `config.yaml`:
```yaml
ai:
  # Thinking/Reasoning (for DeepSeek-R1, Qwen 3, etc.)
  enable_thinking: true
  thinking_effort: "medium"  # "low", "medium", "high"

  # Model memory management
  model_keep_alive: "10m"

  # Embedding optimization
  embedder_dimensions: 0      # 0 = full dimensions
  embedder_truncate: true     # Truncate large inputs

  # Structured output
  enforce_json_output: false  # Experimental
```

---

## Testing Plan

1. **Thinking Mode**
   - Test with DeepSeek-R1 for code review
   - Verify thinking traces are captured in logs
   - Compare review quality with/without thinking

2. **KeepAlive**
   - Measure cold-start vs warm response times
   - Test with multiple sequential reviews
   - Monitor memory usage

3. **Embedding Options**
   - Test dimension reduction (512, 256)
   - Verify search quality metrics
   - Benchmark embedding speed

---

## Breaking Changes
None - all new features are additive and backward compatible.