package question

import (
	"context"
	"log/slog"
	"testing"

	"github.com/sevigo/goframe/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/sevigo/code-warden/internal/llm"
	"github.com/sevigo/code-warden/mocks"
)

func TestAnswerQuestion(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockVS := mocks.NewMockVectorStore(ctrl)
	mockSVS := mocks.NewMockScopedVectorStore(ctrl)
	mockLLM := mocks.NewMockModel(ctrl)

	pm, err := llm.NewPromptManager()
	require.NoError(t, err)

	cfg := Config{
		VectorStore:  mockVS,
		GeneratorLLM: mockLLM,
		PromptMgr:    pm,
		Logger:       slog.Default(),
		ContextFormat: func(_ []schema.Document) string {
			return "some context"
		},
	}
	svc := NewService(cfg)

	question := "What is this?"
	collection := "coll"
	model := "model"

	mockVS.EXPECT().ForRepo(collection, model).Return(mockSVS)
	mockSVS.EXPECT().SimilaritySearch(gomock.Any(), question, gomock.Any(), gomock.Any()).Return([]schema.Document{{PageContent: "doc1"}}, nil)

	mockLLM.EXPECT().Call(gomock.Any(), gomock.Any(), gomock.Any()).Return("The answer", nil)

	ans, err := svc.AnswerQuestion(context.Background(), collection, model, question, nil)
	assert.NoError(t, err)
	assert.Equal(t, "The answer", ans)
}

func TestAnswerWithValidation(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockVS := mocks.NewMockVectorStore(ctrl)
	mockSVS := mocks.NewMockScopedVectorStore(ctrl)
	mockGenLLM := mocks.NewMockModel(ctrl)
	mockValLLM := mocks.NewMockModel(ctrl)

	pm, err := llm.NewPromptManager()
	require.NoError(t, err)

	cfg := Config{
		VectorStore:  mockVS,
		GeneratorLLM: mockGenLLM,
		ValidatorLLM: mockValLLM,
		PromptMgr:    pm,
		Logger:       slog.Default(),
		ContextFormat: func(_ []schema.Document) string {
			return "some context"
		},
	}
	svc := NewService(cfg)

	question := "What is this?"
	collection := "coll"
	model := "model"

	mockVS.EXPECT().ForRepo(collection, model).Return(mockSVS)
	// SimilaritySearch for validation call
	mockSVS.EXPECT().SimilaritySearch(gomock.Any(), question, gomock.Any(), gomock.Any()).Return([]schema.Document{{PageContent: "relevant doc"}}, nil)

	// Validation call (GeneratorLLM is used for the prompt generation, then ValidatorLLM for filtering)
	// Actually, AnswerWithValidation calls answerWithoutValidation which uses RetrievalQA chain.

	// The implementation of AnswerWithValidation:
	// 1. validatorLLM.Call for validation (filter irrelevant)
	// 2. AnswerWithoutValidation for the final answer

	mockValLLM.EXPECT().Call(gomock.Any(), gomock.Any(), gomock.Any()).Return("yes", nil)
	mockGenLLM.EXPECT().Call(gomock.Any(), gomock.Any(), gomock.Any()).Return("Final Answer", nil)

	ans, err := svc.AnswerQuestion(context.Background(), collection, model, question, nil)
	assert.NoError(t, err)
	assert.Equal(t, "Final Answer", ans)
}
