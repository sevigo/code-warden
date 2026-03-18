package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sevigo/goframe/parsers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefinitionExtractor_GoStruct(t *testing.T) {
	goCode := `package main

// Service represents a service interface.
type Service interface {
	DoSomething(ctx context.Context) error
	ProcessData(data []byte) ([]byte, error)
}

// Config holds configuration options.
type Config struct {
	Name    string
	Timeout int
}

func NewService(cfg Config) Service {
	return &service{cfg: cfg}
}
`

	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "service.go")
	err := os.WriteFile(tmpFile, []byte(goCode), 0644)
	require.NoError(t, err)

	extractor := NewDefinitionExtractor(parsers.NewRegistry(nil), nil)
	docs := extractor.ExtractDefinitions(context.Background(), tmpFile, "service.go", []byte(goCode))

	// Should find: Service interface, Config struct, NewService func
	assert.GreaterOrEqual(t, len(docs), 1, "should extract at least one definition")

	// Check that definitions have correct metadata
	for _, doc := range docs {
		assert.Equal(t, "definition", doc.Metadata["chunk_type"])
		assert.Equal(t, "service.go", doc.Metadata["source"])
		assert.Equal(t, "main", doc.Metadata["package_name"])
		assert.True(t, doc.Metadata["is_exported"].(bool))
	}
}

func TestDefinitionExtractor_TypeScriptClass(t *testing.T) {
	tsCode := `import { Service } from './service';

export interface IHandler {
    handle(request: Request): Response;
}

export class ApiHandler implements IHandler {
    constructor(private service: Service) {}

    async handle(request: Request): Promise<Response> {
        return this.service.process(request);
    }
}
`

	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "handler.ts")
	err := os.WriteFile(tmpFile, []byte(tsCode), 0644)
	require.NoError(t, err)

	extractor := NewDefinitionExtractor(nil, nil) // No parser, use regex fallback
	docs := extractor.ExtractDefinitions(context.Background(), tmpFile, "handler.ts", []byte(tsCode))

	assert.GreaterOrEqual(t, len(docs), 1, "should extract definitions via regex")

	// Check metadata
	for _, doc := range docs {
		assert.Equal(t, "definition", doc.Metadata["chunk_type"])
		assert.Contains(t, []string{"IHandler", "ApiHandler"}, doc.Metadata["identifier"])
	}
}

func TestDefinitionExtractor_PythonClass(t *testing.T) {
	pythonCode := `"""Service module."""

class BaseService:
    """Base class for services."""
    
    def __init__(self, config):
        self.config = config
    
    def process(self, data):
        return data

class HttpClient(BaseService):
    """HTTP client implementation."""
    
    def send_request(self, url):
        pass
`

	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "service.py")
	err := os.WriteFile(tmpFile, []byte(pythonCode), 0644)
	require.NoError(t, err)

	extractor := NewDefinitionExtractor(nil, nil) // No parser, use regex fallback
	docs := extractor.ExtractDefinitions(context.Background(), tmpFile, "service.py", []byte(pythonCode))

	assert.GreaterOrEqual(t, len(docs), 1, "should extract class definitions")

	// Check metadata
	classNames := make([]string, 0, len(docs))
	for _, doc := range docs {
		assert.Equal(t, "definition", doc.Metadata["chunk_type"])
		if doc.Metadata["kind"] == "class" {
			classNames = append(classNames, doc.Metadata["identifier"].(string))
		}
	}

	assert.Contains(t, classNames, "BaseService")
}

func TestDefinitionExtractor_GoFunctionSignature(t *testing.T) {
	goCode := `package main

// ProcessFile reads and processes a file.
func ProcessFile(ctx context.Context, path string) ([]byte, error) {
    data, err := os.ReadFile(path)
    if err != nil {
        return nil, err
    }
    return processData(data), nil
}
`

	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "process.go")
	err := os.WriteFile(tmpFile, []byte(goCode), 0644)
	require.NoError(t, err)

	extractor := NewDefinitionExtractor(nil, nil)
	docs := extractor.ExtractDefinitions(context.Background(), tmpFile, "process.go", []byte(goCode))

	assert.GreaterOrEqual(t, len(docs), 1, "should extract function definition")

	// Find ProcessFile definition
	var foundProcessFile bool
	for _, doc := range docs {
		if doc.Metadata["identifier"] == "ProcessFile" {
			foundProcessFile = true
			assert.Equal(t, "func", doc.Metadata["kind"])
			assert.Contains(t, doc.PageContent, "ProcessFile")
			break
		}
	}
	assert.True(t, foundProcessFile, "should find ProcessFile function")
}

func TestDefinitionExtractor_PrivateSymbols(t *testing.T) {
	goCode := `package main

// Public function
func PublicFunction() {}

// private function (not exported)
func privateFunction() {}

// Public struct
type PublicStruct struct {
    Name string
}

// private struct (not exported)
type privateStruct struct {
    id int
}
`

	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "symbols.go")
	err := os.WriteFile(tmpFile, []byte(goCode), 0644)
	require.NoError(t, err)

	extractor := NewDefinitionExtractor(nil, nil)
	docs := extractor.ExtractDefinitions(context.Background(), tmpFile, "symbols.go", []byte(goCode))

	// Should only extract public symbols
	identifiers := make([]string, 0, len(docs))
	for _, doc := range docs {
		identifiers = append(identifiers, doc.Metadata["identifier"].(string))
	}

	assert.Contains(t, identifiers, "PublicFunction")
	assert.Contains(t, identifiers, "PublicStruct")
	assert.NotContains(t, identifiers, "privateFunction")
	assert.NotContains(t, identifiers, "privateStruct")
}

func TestDefinitionExtractor_JavaClass(t *testing.T) {
	javaCode := `package com.example;

public class UserService {
    private String name;
    
    public UserService(String name) {
        this.name = name;
    }
    
    public String getName() {
        return name;
    }
}

interface Service {
    void process();
}
`

	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "UserService.java")
	err := os.WriteFile(tmpFile, []byte(javaCode), 0644)
	require.NoError(t, err)

	extractor := NewDefinitionExtractor(nil, nil)
	docs := extractor.ExtractDefinitions(context.Background(), tmpFile, "UserService.java", []byte(javaCode))

	assert.GreaterOrEqual(t, len(docs), 1, "should extract Java definitions")

	identifiers := make([]string, 0, len(docs))
	for _, doc := range docs {
		identifiers = append(identifiers, doc.Metadata["identifier"].(string))
	}

	assert.Contains(t, identifiers, "UserService")
}

func TestDefinitionExtractor_RustStruct(t *testing.T) {
	rustCode := `pub struct Config {
    name: String,
    timeout: u64,
}

pub trait Service {
    fn process(&self, data: &[u8]) -> Vec<u8>;
}

pub fn new_service(cfg: Config) -> Box<dyn Service> {
    // implementation
}

enum Status {
    Active,
    Inactive,
}
`

	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "service.rs")
	err := os.WriteFile(tmpFile, []byte(rustCode), 0644)
	require.NoError(t, err)

	extractor := NewDefinitionExtractor(nil, nil)
	docs := extractor.ExtractDefinitions(context.Background(), tmpFile, "service.rs", []byte(rustCode))

	assert.GreaterOrEqual(t, len(docs), 1, "should extract Rust definitions")

	// Check for pub symbols (exported)
	foundStruct := false
	for _, doc := range docs {
		if doc.Metadata["identifier"] == "Config" && doc.Metadata["kind"] == "struct" {
			foundStruct = true
			break
		}
	}
	assert.True(t, foundStruct, "should find Config struct")
}

func TestDefinitionExtractor_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "empty.go")
	err := os.WriteFile(tmpFile, []byte("package main\n"), 0644)
	require.NoError(t, err)

	extractor := NewDefinitionExtractor(nil, nil)
	docs := extractor.ExtractDefinitions(context.Background(), tmpFile, "empty.go", []byte("package main\n"))

	assert.Equal(t, 0, len(docs), "empty file should have no definitions")
}

func TestDefinitionExtractor_UnsupportedExtension(t *testing.T) {
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "data.txt")
	err := os.WriteFile(tmpFile, []byte("some content"), 0644)
	require.NoError(t, err)

	extractor := NewDefinitionExtractor(nil, nil)
	docs := extractor.ExtractDefinitions(context.Background(), tmpFile, "data.txt", []byte("some content"))

	assert.Nil(t, docs, "unsupported extension should return nil")
}

func TestIsExported(t *testing.T) {
	tests := []struct {
		name     string
		symbol   string
		ext      string
		expected bool
	}{
		{"Go exported", "Service", ".go", true},
		{"Go unexported", "service", ".go", false},
		{"Java class", "UserService", ".java", true},
		{"Python public", "process_data", ".py", true},
		{"Python dunder", "__init__", ".py", false},
		{"TypeScript exported", "Component", ".ts", true},
		{"TypeScript private", "helper", ".ts", false},
		{"Rust type", "Config", ".rs", true},
		{"Rust function", "new_service", ".rs", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isExported(tt.symbol, tt.ext)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCountLines(t *testing.T) {
	tests := []struct {
		input    string
		expected int
	}{
		{"", 1},
		{"single line", 1},
		{"line1\nline2", 2},
		{"line1\nline2\nline3", 3},
		{"\n\n\n", 4},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := countLines(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestExtractCompleteDefinition(t *testing.T) {
	content := `package main

type Service interface {
	DoSomething(ctx context.Context) error
	Process(data []byte) ([]byte, error)
	Close() error
}

func main() {}
`

	start := len("package main\n\n")
	end := start + 50

	result := extractCompleteDefinition(content, start, end, "interface")
	assert.Contains(t, result, "type Service interface")
}
