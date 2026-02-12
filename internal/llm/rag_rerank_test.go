package llm

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/sevigo/goframe/schema"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/mocks"
)

func TestPerformSingleHyDEJob_Reranking(t *testing.T) {
	testCases := []struct {
		name              string
		mockSetup         func(vs *mocks.MockVectorStore, sVS *mocks.MockScopedVectorStore, rr *mocks.MockReranker)
		expectedDocsCount int
	}{
		{
			name: "Success: Recall 20 -> Rerank Top 5",
			mockSetup: func(vs *mocks.MockVectorStore, sVS *mocks.MockScopedVectorStore, rr *mocks.MockReranker) {
				vs.EXPECT().ForRepo("col", "emb").Return(sVS).AnyTimes()

				recallDocs := make([]schema.Document, 20)
				for i := range 20 {
					recallDocs[i] = schema.Document{PageContent: "doc"}
				}
				sVS.EXPECT().SimilaritySearch(gomock.Any(), gomock.Any(), 20, gomock.Any()).Return(recallDocs, nil).AnyTimes()

				rr.EXPECT().Rerank(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
					func(_ context.Context, _ string, docs []schema.Document) ([]schema.ScoredDocument, error) {
						var scored []schema.ScoredDocument
						for _, d := range docs[:5] {
							scored = append(scored, schema.ScoredDocument{Document: d, Score: 0.9})
						}
						return scored, nil
					},
				).AnyTimes()
			},
			expectedDocsCount: 5,
		},
		{
			name: "Fallback: Reranking Fails -> Return Base Recall (Trimmed to 5)",
			mockSetup: func(vs *mocks.MockVectorStore, sVS *mocks.MockScopedVectorStore, rr *mocks.MockReranker) {
				vs.EXPECT().ForRepo("col", "emb").Return(sVS).AnyTimes()

				recallDocs := make([]schema.Document, 10)
				for i := range 10 {
					recallDocs[i] = schema.Document{PageContent: "doc"}
				}
				sVS.EXPECT().SimilaritySearch(gomock.Any(), gomock.Any(), 20, gomock.Any()).Return(recallDocs, nil).AnyTimes()

				rr.EXPECT().Rerank(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, errors.New("rerank error"))
			},
			expectedDocsCount: 5,
		},
		{
			name: "Precision: Recall returns fewer than TopK -> Rerank all",
			mockSetup: func(vs *mocks.MockVectorStore, sVS *mocks.MockScopedVectorStore, rr *mocks.MockReranker) {
				vs.EXPECT().ForRepo("col", "emb").Return(sVS).AnyTimes()

				recallDocs := make([]schema.Document, 3)
				sVS.EXPECT().SimilaritySearch(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(recallDocs, nil).AnyTimes()

				scoredDocs := make([]schema.ScoredDocument, 3)
				for i := range recallDocs {
					scoredDocs[i] = schema.ScoredDocument{Document: recallDocs[i], Score: 0.8}
				}
				rr.EXPECT().Rerank(gomock.Any(), gomock.Any(), gomock.Any()).Return(scoredDocs, nil).AnyTimes()
			},
			expectedDocsCount: 3,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockVS := mocks.NewMockVectorStore(ctrl)
			mockSVS := mocks.NewMockScopedVectorStore(ctrl)
			mockReranker := mocks.NewMockReranker(ctrl)

			if tc.mockSetup != nil {
				tc.mockSetup(mockVS, mockSVS, mockReranker)
			}

			rag := &ragService{
				cfg:         &config.Config{},
				vectorStore: mockVS,
				reranker:    mockReranker,
				logger:      slog.Default(),
			}

			scopedStore := mockVS.ForRepo("col", "emb")
			results := rag.performSingleHyDEJob(context.Background(), scopedStore, "test query")

			assert.Len(t, results, tc.expectedDocsCount)
		})
	}
}

func TestStripPatchNoise(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"empty", "", ""},
		{"only metadata", "--- a/file.go\n+++ b/file.go\n@@ -1,2 +1,3 @@\n", ""},
		{"mixed", "--- a/file.go\n+func X() {}\n- func Y() {}\n@@ -1,2 +1,3 @@\n", "+func X() {}"},
		{"hyde header + patch", "To understand... \n+func X() {}\n", "To understand... \n+func X() {}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripPatchNoise(tt.input)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestPreFilterBM25(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		docs      []schema.Document
		topK      int
		expectLen int
	}{
		{"topK >= len", "foo", make([]schema.Document, 3), 5, 3},
		{"topK < len", "foo bar", make([]schema.Document, 10), 5, 5},
		{"empty query", "", make([]schema.Document, 5), 3, 5}, // preFilterBM25 returns docs if query is empty or less than 3 chars terms
		{"empty docs", "foo", []schema.Document{}, 3, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := preFilterBM25(tt.query, tt.docs, tt.topK)
			if len(got) != tt.expectLen {
				t.Errorf("preFilterBM25() len = %d, want %d", len(got), tt.expectLen)
			}
		})
	}
}
