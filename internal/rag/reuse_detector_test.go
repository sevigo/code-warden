package rag

import (
	"context"
	"strings"
	"testing"

	"github.com/sevigo/goframe/llms"
	"github.com/sevigo/goframe/schema"
	"github.com/sevigo/goframe/vectorstores"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	internalgithub "github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/storage"
)

// mockLLM is a mock LLM Model for testing
type mockLLM struct {
	mock.Mock
}

func (m *mockLLM) Call(ctx context.Context, prompt string, options ...llms.CallOption) (string, error) {
	args := m.Called(ctx, prompt)
	return args.String(0), args.Error(1)
}

func (m *mockLLM) GenerateContent(ctx context.Context, messages []schema.MessageContent, options ...llms.CallOption) (*schema.ContentResponse, error) {
	args := m.Called(ctx, messages)
	if resp := args.Get(0); resp != nil {
		return resp.(*schema.ContentResponse), args.Error(1)
	}
	return nil, args.Error(1)
}

type mockVectorStore struct {
	vectorstores.VectorStore
	mock.Mock
}

func (m *mockVectorStore) SimilaritySearch(ctx context.Context, query string, numDocs int, opts ...vectorstores.Option) ([]schema.Document, error) {
	args := m.Called(ctx, query, numDocs, opts)
	return args.Get(0).([]schema.Document), args.Error(1)
}

type mockScopedStore struct {
	storage.ScopedVectorStore
	mock.Mock
}

func (m *mockScopedStore) SimilaritySearch(ctx context.Context, query string, numDocs int, opts ...vectorstores.Option) ([]schema.Document, error) {
	// we just ignore opts in the mock match for simplicity, or we can use mock.Anything
	args := m.Called(ctx, query, numDocs)
	return args.Get(0).([]schema.Document), args.Error(1)
}

type mockStorageVectorStore struct {
	storage.VectorStore
	mock.Mock
}

func (m *mockStorageVectorStore) ForRepo(collectionName string, embedderModelName string) storage.ScopedVectorStore {
	args := m.Called(collectionName, embedderModelName)
	return args.Get(0).(storage.ScopedVectorStore)
}

func TestExtractAddedFunctions(t *testing.T) {
	detector := &reuseDetector{}

	changedFiles := []internalgithub.ChangedFile{
		{
			Filename: "pkg/utils/sanitize.go",
			Patch: `@@ -10,5 +10,12 @@
-func old() {}
+func SanitizeEmail(e string) string {
+	e = strings.TrimSpace(e)
+	return strings.ToLower(e)
+}
+
 func keep() {}
+func Another(a int) {
+	fmt.Println(a)
+}
`,
		},
	}

	funcs := detector.extractAddedFunctions(changedFiles)
	require.Len(t, funcs, 2)

	assert.Equal(t, "pkg/utils/sanitize.go", funcs[0].File)
	assert.Equal(t, 10, funcs[0].Line)
	assert.Contains(t, funcs[0].Code, "func SanitizeEmail(e string) string {")
	assert.Contains(t, funcs[0].Code, "return strings.ToLower(e)")
	assert.Contains(t, funcs[0].Code, "}")

	assert.Equal(t, "pkg/utils/sanitize.go", funcs[1].File)
	assert.Equal(t, 15, funcs[1].Line) // approximately
	assert.Contains(t, funcs[1].Code, "func Another(a int) {")
	assert.Contains(t, funcs[1].Code, "}")
}

func TestReuseDetector_Detect(t *testing.T) {
	mockL := new(mockLLM)
	mockVS := new(mockStorageVectorStore)
	mockScoped := new(mockScopedStore)

	detector := NewReuseDetector(mockL, mockVS)

	changedFiles := []internalgithub.ChangedFile{
		{
			Filename: "main.go",
			Patch: `@@ -1,1 +1,3 @@
+func cleanupEmail(email string) string {
+   return strings.ToLower(strings.TrimSpace(email))
+}
`,
		},
	}

	repo := &storage.Repository{
		QdrantCollectionName: "test-col",
		EmbedderModelName:    "test-embed",
	}

	mockVS.On("ForRepo", "test-col", "test-embed").Return(mockScoped)

	// Mock intent extraction
	mockL.On("GenerateContent", mock.Anything, mock.MatchedBy(func(messages []schema.MessageContent) bool {
		if len(messages) == 0 {
			return false
		}
		prompt := ""
		for _, p := range messages[0].Parts {
			if txt, ok := p.(schema.TextContent); ok {
				prompt += txt.Text
			}
		}
		return strings.Contains(prompt, "Write a short, natural language sentence describing") && strings.Contains(prompt, "cleanupEmail")
	})).Return(&schema.ContentResponse{
		Choices: []*schema.ContentChoice{
			{Content: "Validates and cleans up an email address"},
		},
	}, nil)

	// Mock VectorStore search
	mockScoped.On("SimilaritySearch", mock.Anything, "Validates and cleans up an email address", 3).Return([]schema.Document{
		{
			PageContent: "func SanitizeEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }",
			Metadata:    map[string]any{"source": "pkg/utils/sanitize.go"},
		},
	}, nil)

	// Mock Verify Match (Judge)
	mockL.On("GenerateContent", mock.Anything, mock.MatchedBy(func(messages []schema.MessageContent) bool {
		if len(messages) == 0 {
			return false
		}
		prompt := ""
		for _, p := range messages[0].Parts {
			if txt, ok := p.(schema.TextContent); ok {
				prompt += txt.Text
			}
		}
		return strings.Contains(prompt, "Does the existing code \"B\" provide the same functionality as the new code \"A\"")
	})).Return(&schema.ContentResponse{
		Choices: []*schema.ContentChoice{
			{Content: `{ "is_match": true, "confidence": 0.95 }`},
		},
	}, nil)

	suggestions, err := detector.Detect(context.Background(), repo, changedFiles)
	require.NoError(t, err)
	require.Len(t, suggestions, 1)

	assert.Equal(t, "main.go", suggestions[0].FilePath)
	assert.Equal(t, 1, suggestions[0].Line)
	assert.Contains(t, suggestions[0].Message, "pkg/utils/sanitize.go")
	assert.Contains(t, suggestions[0].Message, "0.95")

	mockL.AssertExpectations(t)
	mockVS.AssertExpectations(t)
	mockScoped.AssertExpectations(t)
}
