package index

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
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
		EmbedderModelName:    "test_model",
	}

	// Expectations
	mockStore.EXPECT().GetFilesForRepo(gomock.Any(), repo.ID).Return(make(map[string]storage.FileRecord), nil)
	mockVS.EXPECT().ForRepo(repo.QdrantCollectionName, repo.EmbedderModelName).Return(mockSVS)
	mockSVS.EXPECT().AddDocuments(gomock.Any(), gomock.Any()).Return([]string{"id1"}, nil)
	mockStore.EXPECT().UpsertFiles(gomock.Any(), repo.ID, gomock.Any()).Return(nil)

	cfg := Config{
		Store:          mockStore,
		VectorStore:    mockVS,
		Splitter:       &mockSplitter{},
		ParserRegistry: parsers.NewRegistry(slog.Default()),
		Logger:         slog.Default(),
	}
	indexer := New(cfg)

	err = indexer.SetupRepoContext(context.Background(), nil, repo, repoDir)
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
	}
	indexer := New(cfg)

	err = indexer.SetupRepoContext(context.Background(), nil, repo, repoDir)
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
		EmbedderModelName:    "test_model",
	}

	// Database has a file that is no longer on disk
	staleFile := "deleted.go"
	mockStore.EXPECT().GetFilesForRepo(gomock.Any(), repo.ID).Return(map[string]storage.FileRecord{
		staleFile: {FilePath: staleFile, FileHash: "somehash"},
	}, nil)

	mockVS.EXPECT().ForRepo(repo.QdrantCollectionName, repo.EmbedderModelName).Return(mocks.NewMockScopedVectorStore(ctrl))

	// Pruning expectations
	mockStore.EXPECT().DeleteFiles(gomock.Any(), repo.ID, []string{staleFile}).Return(nil)
	mockVS.EXPECT().DeleteDocumentsFromCollectionByFilter(gomock.Any(), repo.QdrantCollectionName, repo.EmbedderModelName, gomock.Any()).Return(nil)

	cfg := Config{
		Store:          mockStore,
		VectorStore:    mockVS,
		Splitter:       &mockSplitter{},
		ParserRegistry: parsers.NewRegistry(slog.Default()),
		Logger:         slog.Default(),
	}
	indexer := New(cfg)

	err := indexer.SetupRepoContext(context.Background(), nil, repo, repoDir)
	assert.NoError(t, err)
}

func TestUpdateRepoContext(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStore := mocks.NewMockStore(ctrl)
	mockVS := mocks.NewMockVectorStore(ctrl)
	mockSVS := mocks.NewMockScopedVectorStore(ctrl)

	repoDir := t.TempDir()
	repo := &storage.Repository{ID: 1, QdrantCollectionName: "test_coll", EmbedderModelName: "test_model"}

	filesToProcess := []string{"new.go"}
	filesToDelete := []string{"old.go"}

	fullPath := filepath.Join(repoDir, "new.go")
	require.NoError(t, os.WriteFile(fullPath, []byte("package new\n"), 0644))

	// Expectations
	mockVS.EXPECT().DeleteDocumentsFromCollection(gomock.Any(), repo.QdrantCollectionName, repo.EmbedderModelName, filesToDelete).Return(nil)
	mockVS.EXPECT().ForRepo(repo.QdrantCollectionName, repo.EmbedderModelName).Return(mockSVS)
	mockSVS.EXPECT().AddDocuments(gomock.Any(), gomock.Any()).Return([]string{"id2"}, nil)
	mockStore.EXPECT().UpsertFiles(gomock.Any(), repo.ID, gomock.Any()).Return(nil)

	cfg := Config{
		Store:          mockStore,
		VectorStore:    mockVS,
		Splitter:       &mockSplitter{},
		ParserRegistry: parsers.NewRegistry(slog.Default()),
		Logger:         slog.Default(),
	}
	indexer := New(cfg)

	err := indexer.UpdateRepoContext(context.Background(), nil, repo, repoDir, filesToProcess, filesToDelete)
	assert.NoError(t, err)
}
