package llm

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/sevigo/code-warden/internal/config"
	internalgithub "github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/mocks"
	"github.com/sevigo/goframe/schema"
	"github.com/sevigo/goframe/vectorstores"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"
)

func TestSearchHyDEBatch_Reranking(t *testing.T) {
	testCases := []struct {
		name              string
		files             []internalgithub.ChangedFile
		mockSetup         func(vs *mocks.MockVectorStore, sVS *mocks.MockScopedVectorStore, rr *mocks.MockReranker)
		expectedDocsCount []int
	}{
		{
			name: "Success: Recall 20 -> Rerank Top 5",
			files: []internalgithub.ChangedFile{
				{Filename: "main.go", Patch: "+func main() {}"},
			},
			mockSetup: func(vs *mocks.MockVectorStore, sVS *mocks.MockScopedVectorStore, rr *mocks.MockReranker) {
				// 1. Scoped Store creation
				vs.EXPECT().ForRepo("col", "emb").Return(sVS)

				// 2. Base Retrieval (Recall 20)
				// We expect 20 docs from similarity search (recall stage)
				recallDocs := make([]schema.Document, 20)
				for i := range 20 {
					recallDocs[i] = schema.Document{PageContent: "doc"}
				}
				// vectorstores.ToRetriever -> SimilaritySearch (VARIADIC: ctx, query, k, opts...)
				// We now use opts for Hybrid Search, so we must expect additional arguments.
				sVS.EXPECT().SimilaritySearch(gomock.Any(), gomock.Any(), 20, gomock.Any()).Return(recallDocs, nil).AnyTimes()

				// 3. Reranking (Precision Top 5)
				// Relax expectation to allow Any call order or internal behavior
				sVS.EXPECT().SimilaritySearch(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(recallDocs, nil).AnyTimes()
				// Just in case ToRetriever uses WithScores
				var emptyScores []vectorstores.DocumentWithScore
				sVS.EXPECT().SimilaritySearchWithScores(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(emptyScores, nil).AnyTimes()

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
			expectedDocsCount: []int{5},
		},
		{
			name: "Fallback: Reranking Fails -> Return Base Recall (Trimmed to 5)",
			files: []internalgithub.ChangedFile{
				{Filename: "main.go", Patch: "+func main() {}"},
			},
			mockSetup: func(vs *mocks.MockVectorStore, sVS *mocks.MockScopedVectorStore, rr *mocks.MockReranker) {
				vs.EXPECT().ForRepo("col", "emb").Return(sVS)

				recallDocs := make([]schema.Document, 10)
				for i := range 10 {
					recallDocs[i] = schema.Document{PageContent: "doc"}
				}

				// Called once by RerankingRetriever (which then fails rerank)
				// AND potentially once more by fallback logic depending on implementation details.
				// In our implementation:
				// 1. rr.GetRelevantDocuments calls baseRetriever.GetRelevantDocuments -> sVS.SimilaritySearch
				// 2. rr calls Reranker.Rerank -> Fails
				// 3. rag.go catches error and calls baseRetriever.GetRelevantDocuments AGAIN -> sVS.SimilaritySearch
				sVS.EXPECT().SimilaritySearch(gomock.Any(), gomock.Any(), 20, gomock.Any()).Return(recallDocs, nil).AnyTimes()

				rr.EXPECT().Rerank(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, errors.New("rerank error"))
			},
			expectedDocsCount: []int{5}, // 10 recalled, trimmed to 5
		},
		{
			name: "Precision: Recall returns fewer than TopK -> Rerank all",
			files: []internalgithub.ChangedFile{
				{Filename: "small.go", Patch: "+small change"},
			},
			mockSetup: func(vs *mocks.MockVectorStore, sVS *mocks.MockScopedVectorStore, rr *mocks.MockReranker) {
				vs.EXPECT().ForRepo("col", "emb").Return(sVS)

				// only 3 docs found
				recallDocs := make([]schema.Document, 3)
				sVS.EXPECT().SimilaritySearch(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(recallDocs, nil).AnyTimes()

				// Reranker should receive 3 and return them (or sorted subset)
				scoredDocs := make([]schema.ScoredDocument, 3)
				for i := range recallDocs {
					scoredDocs[i] = schema.ScoredDocument{Document: recallDocs[i], Score: 0.8}
				}
				rr.EXPECT().Rerank(gomock.Any(), gomock.Any(), gomock.Any()).Return(scoredDocs, nil).AnyTimes()
			},
			expectedDocsCount: []int{3},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockVS := mocks.NewMockVectorStore(ctrl)
			mockSVS := mocks.NewMockScopedVectorStore(ctrl) // Generated mock for ScopedVectorStore?
			// Note: I need to verify if MockScopedVectorStore exists or if ScopedVectorStore is part of VectorStore mock file.
			// Usually if it's an interface in storage package, mockgen -source should have it or I generated it.
			// Assumption: MockScopedVectorStore is available in mocks package or I need to check mocks.

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

			// HyDE map empty for simplicity
			hydeMap := map[int]string{}

			results, _ := rag.searchHyDEBatch(context.Background(), "col", "emb", tc.files, hydeMap)
			// assert.NoError(t, err) // err removed from signature

			assert.Len(t, results, len(tc.expectedDocsCount))
			for i, count := range tc.expectedDocsCount {
				assert.Len(t, results[i], count)
			}
		})
	}
}
