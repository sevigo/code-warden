package warden

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/sevigo/goframe/agent"
	"github.com/sevigo/goframe/llms"
	"github.com/sevigo/goframe/schema"

	"github.com/sevigo/code-warden/internal/storage"
)

// ExplorerConfig holds configuration for the codebase explorer.
type ExplorerConfig struct {
	LLM           llms.Model
	VectorStore   storage.VectorStore
	Store         storage.Store
	Logger        *slog.Logger
	EmbedderModel string
	SearchCode    SearchCodeFunc
	GetStructure  GetStructureFunc
}

// Explorer uses an embedded agent to analyze codebase patterns and generate design documents.
type Explorer struct {
	cfg        ExplorerConfig
	registry   *agent.Registry
	governance *agent.Governance
}

// NewExplorer creates a new codebase explorer.
func NewExplorer(cfg ExplorerConfig) (*Explorer, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	if cfg.LLM == nil {
		return nil, fmt.Errorf("LLM is required for explorer")
	}

	registry := agent.NewRegistry()
	toolList := buildExplorerTools(cfg)
	for _, t := range toolList {
		if err := registry.Register(t); err != nil {
			cfg.Logger.Warn("failed to register tool", "tool", t.Name(), "error", err)
		}
	}

	allowedTools := map[string]bool{
		"search_code":   true,
		"get_structure": true,
	}
	permissionCheck := &agent.PermissionCheck{
		Allowed: allowedTools,
	}
	governance := agent.NewGovernance(permissionCheck)

	return &Explorer{
		cfg:        cfg,
		registry:   registry,
		governance: governance,
	}, nil
}

// ExploreCodebase analyzes the codebase and generates design documents.
func (e *Explorer) ExploreCodebase(ctx context.Context, collectionName, repoOwner, repoName, repoPath string) (*DesignDocuments, error) {
	e.cfg.Logger.Info("starting codebase exploration",
		"collection", collectionName,
		"repo", repoOwner+"/"+repoName)

	systemPrompt := buildExplorationPrompt(repoPath, collectionName)

	loop, err := agent.NewAgentLoop(e.cfg.LLM, e.registry,
		agent.WithLoopSystemPrompt(systemPrompt),
		agent.WithLoopMaxIterations(20),
		agent.WithLoopGovernance(e.governance),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create agent loop: %w", err)
	}

	task := agent.Task{
		ID:          fmt.Sprintf("explore-%s-%s", repoOwner, repoName),
		Description: "Explore codebase patterns and generate design documents",
		Context: fmt.Sprintf("Repository: %s/%s\nCollection: %s\nPath: %s\n\nIMPORTANT: Always pass collection_name=\"%s\" to all tool calls.",
			repoOwner, repoName, collectionName, repoPath, collectionName),
		Priority: 5,
	}

	result, err := loop.Run(ctx, task, nil)
	if err != nil {
		return nil, fmt.Errorf("exploration failed: %w", err)
	}

	docs := e.parseExplorationResult(result.Response, repoOwner, repoName)

	totalTokens := result.Tokens.Input + result.Tokens.Output
	e.cfg.Logger.Info("codebase exploration complete",
		"documents", len(docs.Documents),
		"iterations", result.Iterations,
		"tokens", totalTokens)

	return docs, nil
}

// parseExplorationResult extracts design documents from agent output.
func (e *Explorer) parseExplorationResult(response string, repoOwner, repoName string) *DesignDocuments {
	docs := &DesignDocuments{Documents: []*DesignDocument{}}

	docTypeMap := map[string]DesignDocumentType{
		"testing_patterns": DocTypeTestingPatterns,
		"dependencies":     DocTypeDependencies,
		"conventions":      DocTypeConventions,
		"api_patterns":     DocTypeAPIPatterns,
	}

	blocks := extractDocumentBlocks(response)
	for _, block := range blocks {
		doc := parseDocumentBlock(block, docTypeMap, repoOwner, repoName)
		if doc != nil {
			docs.Documents = append(docs.Documents, doc)
		}
	}

	return docs
}

// parseDocumentBlock parses a single document block into a DesignDocument.
func parseDocumentBlock(block map[string]any, docTypeMap map[string]DesignDocumentType, repoOwner, repoName string) *DesignDocument {
	docTypeStr, ok := block["type"].(string)
	if !ok {
		return nil
	}

	docType, valid := docTypeMap[docTypeStr]
	if !valid {
		return nil
	}

	title, _ := block["title"].(string)
	content, _ := block["content"].(string)
	summary, _ := block["summary"].(string)
	confidence, _ := block["confidence"].(float64)
	generatedBy, _ := block["generated_by"].(string)

	symbols := parseStringSlice(block["symbols"])
	directories := parseStringSlice(block["directories"])

	return &DesignDocument{
		ID:          fmt.Sprintf("%s-%s-%d", docType, repoOwner, time.Now().UnixNano()),
		Type:        docType,
		Title:       title,
		Content:     content,
		Summary:     summary,
		Symbols:     symbols,
		Directories: directories,
		Confidence:  confidence,
		GeneratedAt: time.Now(),
		GeneratedBy: generatedBy,
		RepoOwner:   repoOwner,
		RepoName:    repoName,
	}
}

// parseStringSlice extracts a string slice from an interface slice.
func parseStringSlice(v any) []string {
	if v == nil {
		return nil
	}
	slice, ok := v.([]any)
	if !ok {
		return nil
	}
	var result []string
	for _, item := range slice {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

// extractDocumentBlocks parses document blocks from agent response.
func extractDocumentBlocks(response string) []map[string]any {
	var blocks []map[string]any

	patterns := []string{"<document", "<design_doc"}
	for _, pattern := range patterns {
		blocks = append(blocks, extractBlocksForPattern(response, pattern)...)
	}

	return blocks
}

// extractBlocksForPattern extracts JSON blocks for a given tag pattern.
func extractBlocksForPattern(response, pattern string) []map[string]any {
	var blocks []map[string]any
	startIdx := 0

	for {
		idx := strings.Index(response[startIdx:], pattern)
		if idx == -1 {
			break
		}

		docStart := startIdx + idx
		endIdx := strings.Index(response[docStart:], ">")
		if endIdx == -1 {
			break
		}

		closeTag := "</document>"
		closeIdx := strings.Index(response[docStart:], closeTag)
		if closeIdx == -1 {
			closeTag = "</design_doc>"
			closeIdx = strings.Index(response[docStart:], closeTag)
			if closeIdx == -1 {
				break
			}
		}

		jsonContent := response[docStart+endIdx+1 : docStart+closeIdx]
		jsonContent = strings.TrimSpace(jsonContent)

		if strings.HasPrefix(jsonContent, "{") {
			var block map[string]any
			if err := json.Unmarshal([]byte(jsonContent), &block); err == nil {
				blocks = append(blocks, block)
			}
		}

		startIdx = docStart + closeIdx + len(closeTag)
	}

	return blocks
}

// IndexDesignDocuments indexes the design documents in the vector store.
func (e *Explorer) IndexDesignDocuments(ctx context.Context, collectionName string, docs *DesignDocuments) error {
	if len(docs.Documents) == 0 {
		return nil
	}

	scopedStore := e.cfg.VectorStore.ForRepo(collectionName, e.cfg.EmbedderModel)

	var vectors []schema.Document
	for _, doc := range docs.Documents {
		content := doc.ChunkContent()

		vectorDoc := schema.NewDocument(content, map[string]any{
			"chunk_type":   "design_doc",
			"doc_type":     string(doc.Type),
			"title":        doc.Title,
			"repo_owner":   doc.RepoOwner,
			"repo_name":    doc.RepoName,
			"generated_at": doc.GeneratedAt.Format(time.RFC3339),
			"generated_by": doc.GeneratedBy,
			"confidence":   doc.Confidence,
		})

		vectors = append(vectors, vectorDoc)
		e.cfg.Logger.Debug("prepared document for indexing",
			"type", doc.Type,
			"title", doc.Title,
			"content_len", len(content))
	}

	ids, err := scopedStore.AddDocuments(ctx, vectors)
	if err != nil {
		return fmt.Errorf("failed to index design documents: %w", err)
	}

	for i, id := range ids {
		if i < len(docs.Documents) {
			docs.Documents[i].VectorID = id
		}
	}

	e.cfg.Logger.Info("indexed design documents",
		"count", len(ids),
		"collection", collectionName)

	return nil
}

func buildExplorerTools(cfg ExplorerConfig) []agent.Tool {
	return []agent.Tool{
		NewSearchCodeTool(cfg.SearchCode, cfg.VectorStore, cfg.EmbedderModel, cfg.Logger),
		NewGetStructureTool(cfg.GetStructure, cfg.Logger),
	}
}

func buildExplorationPrompt(repoPath, collectionName string) string {
	return fmt.Sprintf(`You are a codebase analyzer. Your task is to explore the codebase using available tools and generate design documents.

## Repository Context
- Path: %s
- Collection: %s

## Available Tools
- search_code(query, limit, chunk_type): Search code semantically
  IMPORTANT: Always include collection_name parameter
- get_structure(root): Get project structure
  IMPORTANT: Always include collection_name parameter

## Documents to Generate

Generate the following design documents by exploring the codebase:

### 1. Testing Patterns (type: "testing_patterns")
Explore and document:
- What assertion library is used? (assert.Equal, require.NoError, testify, etc.)
- How is mocking done? (gomock, testify/mock, manual interfaces, etc.)
- What's the test structure? (table-driven, AAA, fixtures, etc.)
- How are test fixtures and setup handled?

### 2. Dependencies (type: "dependencies")
Explore and document:
- What are the key dependencies?
- Why are they used?
- Any custom wrappers or abstractions?

### 3. Conventions (type: "conventions")
Explore and document:
- Naming conventions (files, packages, functions)
- File organization patterns
- Error handling patterns
- Logging patterns

### 4. API Patterns (type: "api_patterns")
If applicable, explore and document:
- Authentication patterns
- Authorization patterns
- Error response patterns
- Validation patterns

## Output Format

For each document you discover, output a JSON block:
<document>
{
  "type": "DOCUMENT_TYPE",
  "title": "Document Title",
  "content": "Full markdown content...",
  "summary": "Brief 2-3 sentence summary",
  "symbols": ["RelatedSymbol1", "RelatedSymbol2"],
  "directories": ["dir1/", "dir2/"],
  "confidence": 0.95,
  "generated_by": "model-name"
}
</document>

## Instructions
1. Use search_code to find patterns (e.g., "assert", "mock", "test")
   Example: search_code(query="assert.Equal", collection_name="%s", limit=10)
2. Use get_structure to understand project layout
   Example: get_structure(collection_name="%s")
3. Analyze patterns across multiple files
4. Generate comprehensive documents
5. Be specific - cite actual libraries and patterns found

Start exploring now.`, repoPath, collectionName, collectionName, collectionName)
}

func (d *DesignDocuments) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "DesignDocuments{count=%d}\n", len(d.Documents))
	for _, doc := range d.Documents {
		fmt.Fprintf(&b, "  - %s: %s (confidence=%.2f)\n", doc.Type, doc.Title, doc.Confidence)
	}
	return b.String()
}
