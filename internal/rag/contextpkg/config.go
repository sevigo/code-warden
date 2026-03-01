package contextpkg

import (
	"context"
	"log/slog"

	"github.com/sevigo/goframe/llms"
	"github.com/sevigo/goframe/schema"

	"github.com/sevigo/goframe/contextpacker"
	"github.com/sevigo/goframe/parsers"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/llm"
	"github.com/sevigo/code-warden/internal/storage"
)

// Cache defines a simple generic cache interface.
type Cache interface {
	Load(key string) (any, bool)
	Store(key string, value any)
}

// Config holds dependencies mapping for Context Builder.
type Config struct {
	AIConfig       config.AIConfig
	VectorStore    storage.VectorStore
	PromptMgr      *llm.PromptManager
	ParserRegistry parsers.ParserRegistry
	GeneratorLLM   llms.Model
	GetLLM         func(ctx context.Context, modelName string) (llms.Model, error)
	Reranker       schema.Reranker
	ContextPacker  *contextpacker.Packer
	HyDECache      Cache
	Logger         *slog.Logger
}
