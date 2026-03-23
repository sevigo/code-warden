package index

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sevigo/goframe/parsers"
	"github.com/sevigo/goframe/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/sevigo/code-warden/internal/storage"
	"github.com/sevigo/code-warden/mocks"
)

// mockSplitter is a simple manual mock for textsplitter.TextSplitter
type mockSplitter struct{}

func (m *mockSplitter) SplitDocuments(_ context.Context, docs []schema.Document) ([]schema.Document, error) {
	return docs, nil
}

func TestSetupRepoContext_Basic(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStore := mocks.NewMockStore(ctrl)
	mockVS := mocks.NewMockVectorStore(ctrl)
	mockSVS := mocks.NewMockScopedVectorStore(ctrl)

	// Setup temp repo
	repoDir := t.TempDir()
	testFile := filepath.Join(repoDir, "main.go")
	err := os.WriteFile(testFile, []byte("package main\n\nfunc main() {}\n"), 0644)
	require.NoError(t, err)

	repo := &storage.Repository{
		ID:                   1,
		QdrantCollectionName: "test_coll",
	}

	// Expectations
	mockStore.EXPECT().GetFilesForRepo(gomock.Any(), repo.ID).Return(make(map[string]storage.FileRecord), nil)
	mockVS.EXPECT().ForRepo(repo.QdrantCollectionName, "test_model").Return(mockSVS)
	mockSVS.EXPECT().AddDocuments(gomock.Any(), gomock.Any()).Return([]string{"id1"}, nil)
	mockStore.EXPECT().UpsertFiles(gomock.Any(), repo.ID, gomock.Any()).Return(nil)

	cfg := Config{
		Store:          mockStore,
		VectorStore:    mockVS,
		Splitter:       &mockSplitter{},
		ParserRegistry: parsers.NewRegistry(slog.Default()),
		Logger:         slog.Default(),
		EmbedderModel:  "test_model",
	}
	indexer := New(cfg)

	err = indexer.SetupRepoContext(context.Background(), nil, repo, repoDir, nil)
	assert.NoError(t, err)
}

func TestSetupRepoContext_SmartScan(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStore := mocks.NewMockStore(ctrl)
	mockVS := mocks.NewMockVectorStore(ctrl)

	repoDir := t.TempDir()
	testFile := "main.go"
	fullPath := filepath.Join(repoDir, testFile)
	content := []byte("package main\n")
	err := os.WriteFile(fullPath, content, 0644)
	require.NoError(t, err)

	hash, _ := ComputeFileHash(fullPath)

	repo := &storage.Repository{ID: 1}

	// Smart scan skip expectation
	mockStore.EXPECT().GetFilesForRepo(gomock.Any(), repo.ID).Return(map[string]storage.FileRecord{
		testFile: {FilePath: testFile, FileHash: hash},
	}, nil)

	// ForRepo IS called once to initialize scopedStore
	mockVS.EXPECT().ForRepo(gomock.Any(), gomock.Any()).Return(mocks.NewMockScopedVectorStore(ctrl))

	cfg := Config{
		Store:          mockStore,
		VectorStore:    mockVS,
		Splitter:       &mockSplitter{},
		ParserRegistry: parsers.NewRegistry(slog.Default()),
		Logger:         slog.Default(),
		EmbedderModel:  "test_model",
	}
	indexer := New(cfg)

	err = indexer.SetupRepoContext(context.Background(), nil, repo, repoDir, nil)
	assert.NoError(t, err)
}

func TestSetupRepoContext_Pruning(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStore := mocks.NewMockStore(ctrl)
	mockVS := mocks.NewMockVectorStore(ctrl)

	repoDir := t.TempDir() // Empty repo

	repo := &storage.Repository{
		ID:                   1,
		QdrantCollectionName: "test_coll",
	}

	// Database has a file that is no longer on disk
	staleFile := "deleted.go"
	mockStore.EXPECT().GetFilesForRepo(gomock.Any(), repo.ID).Return(map[string]storage.FileRecord{
		staleFile: {FilePath: staleFile, FileHash: "somehash"},
	}, nil)

	mockVS.EXPECT().ForRepo(repo.QdrantCollectionName, "test_model").Return(mocks.NewMockScopedVectorStore(ctrl))

	// Pruning expectations
	mockStore.EXPECT().DeleteFiles(gomock.Any(), repo.ID, []string{staleFile}).Return(nil)
	mockVS.EXPECT().DeleteDocumentsFromCollectionByFilter(gomock.Any(), repo.QdrantCollectionName, "test_model", gomock.Any()).Return(nil)

	cfg := Config{
		Store:          mockStore,
		VectorStore:    mockVS,
		Splitter:       &mockSplitter{},
		ParserRegistry: parsers.NewRegistry(slog.Default()),
		Logger:         slog.Default(),
		EmbedderModel:  "test_model",
	}
	indexer := New(cfg)

	err := indexer.SetupRepoContext(context.Background(), nil, repo, repoDir, nil)
	assert.NoError(t, err)
}

func TestProcessFile_NoExtension(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStore := mocks.NewMockStore(ctrl)
	mockVS := mocks.NewMockVectorStore(ctrl)

	repoDir := t.TempDir()
	files := []string{"Makefile", "Dockerfile", "main.go"}
	for _, f := range files {
		fullPath := filepath.Join(repoDir, f)
		content := []byte("test content\n")
		require.NoError(t, os.WriteFile(fullPath, content, 0644))
	}

	cfg := Config{
		Store:          mockStore,
		VectorStore:    mockVS,
		Splitter:       &mockSplitter{},
		ParserRegistry: parsers.NewRegistry(slog.Default()),
		Logger:         slog.Default(),
		EmbedderModel:  "test_model",
	}
	indexer := New(cfg)

	for _, f := range files {
		docs := indexer.ProcessFile(context.Background(), repoDir, f)
		assert.NotNil(t, docs)
		for _, doc := range docs {
			language, ok := doc.Metadata["language"].(string)
			assert.True(t, ok)
			if f == "Makefile" || f == "Dockerfile" {
				assert.Equal(t, "", language, "file %s should have empty language", f)
			}
			if f == "main.go" {
				assert.Equal(t, "go", language)
			}
		}
	}
}

func TestGenerateFileSummary_NoExtension(t *testing.T) {
	ext := strings.ToLower(filepath.Ext("Makefile"))
	language := ""
	if len(ext) > 1 {
		language = ext[1:]
	}
	assert.Equal(t, "", language)

	ext = strings.ToLower(filepath.Ext("main.go"))
	language = ""
	if len(ext) > 1 {
		language = ext[1:]
	}
	assert.Equal(t, "go", language)
}

func TestParseFileSummaryResponse(t *testing.T) {
	tests := []struct {
		name         string
		response     string
		wantSummary  string
		wantKeywords []string
		wantExports  []string
	}{
		{
			name:         "full response",
			response:     "PURPOSE: Handles webhook events from GitHub\nEXPORTS: WebhookHandler, ProcessEvent, ValidatePayload\nKEYWORDS: webhook, github, event, handler, payload",
			wantSummary:  "Handles webhook events from GitHub",
			wantKeywords: []string{"webhook", "github", "event", "handler", "payload"},
			wantExports:  []string{"WebhookHandler", "ProcessEvent", "ValidatePayload"},
		},
		{
			name:         "purpose and keywords only",
			response:     "PURPOSE: Main entry point for the application\nKEYWORDS: main, entry, bootstrap",
			wantSummary:  "Main entry point for the application",
			wantKeywords: []string{"main", "entry", "bootstrap"},
			wantExports:  nil,
		},
		{
			name:         "exports only",
			response:     "EXPORTS: Run, Stop, Status",
			wantSummary:  "",
			wantKeywords: nil,
			wantExports:  []string{"Run", "Stop", "Status"},
		},
		{
			name: "multi-line response",
			response: `PURPOSE: Database connection pool manager
EXPORTS: Pool, Connection, Query
KEYWORDS: database, pool, connection, sql, postgres`,
			wantSummary:  "Database connection pool manager",
			wantKeywords: []string{"database", "pool", "connection", "sql", "postgres"},
			wantExports:  []string{"Pool", "Connection", "Query"},
		},
		{
			name:         "empty response",
			response:     "",
			wantSummary:  "",
			wantKeywords: nil,
			wantExports:  nil,
		},
		{
			name:         "whitespace in values",
			response:     "PURPOSE:   Some purpose with spaces  \nKEYWORDS:  tag1 ,  tag2 , tag3 ",
			wantSummary:  "Some purpose with spaces",
			wantKeywords: []string{"tag1", "tag2", "tag3"},
			wantExports:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summary, keywords, exports := parseFileSummaryResponse(tt.response)
			assert.Equal(t, tt.wantSummary, summary)
			assert.Equal(t, tt.wantKeywords, keywords)
			assert.Equal(t, tt.wantExports, exports)
		})
	}
}

func TestUpdateRepoContext(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStore := mocks.NewMockStore(ctrl)
	mockVS := mocks.NewMockVectorStore(ctrl)
	mockSVS := mocks.NewMockScopedVectorStore(ctrl)

	repoDir := t.TempDir()
	repo := &storage.Repository{ID: 1, QdrantCollectionName: "test_coll"}

	filesToProcess := []string{"new.go"}
	filesToDelete := []string{"old.go"}

	fullPath := filepath.Join(repoDir, "new.go")
	require.NoError(t, os.WriteFile(fullPath, []byte("package new\n\nfunc DoWork() error { return nil }\n"), 0644))

	// Expectations
	mockVS.EXPECT().DeleteDocumentsFromCollection(gomock.Any(), repo.QdrantCollectionName, "test_model", filesToDelete).Return(nil)
	mockVS.EXPECT().ForRepo(repo.QdrantCollectionName, "test_model").Return(mockSVS)
	mockSVS.EXPECT().AddDocuments(gomock.Any(), gomock.Any()).Return([]string{"id2"}, nil)
	mockStore.EXPECT().UpsertFiles(gomock.Any(), repo.ID, gomock.Any()).Return(nil)

	cfg := Config{
		Store:          mockStore,
		VectorStore:    mockVS,
		Splitter:       &mockSplitter{},
		ParserRegistry: parsers.NewRegistry(slog.Default()),
		Logger:         slog.Default(),
		EmbedderModel:  "test_model",
	}
	indexer := New(cfg)

	err := indexer.UpdateRepoContext(context.Background(), nil, repo, repoDir, filesToProcess, filesToDelete)
	assert.NoError(t, err)
}
