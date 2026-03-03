package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHelloHandler_Handle(t *testing.T) {
	tests := []struct {
		name           string
		expectedStatus int
	}{
		{
			name:           "returns successful hello response",
			expectedStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewHelloHandler(nil)
			req := httptest.NewRequest(http.MethodGet, "/api/v1/hello", nil)
			rec := httptest.NewRecorder()

			handler.Handle(context.Background(), rec, req)

			assert.Equal(t, tt.expectedStatus, rec.Code)
			assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

			var response HelloResponse
			err := json.Unmarshal(rec.Body.Bytes(), &response)
			require.NoError(t, err)

			assert.Equal(t, "Hello from Code-Warden!", response.Message)
			assert.NotEmpty(t, response.Timestamp)

			_, err = time.Parse(time.RFC3339, response.Timestamp)
			assert.NoError(t, err, "timestamp should be valid RFC3339 format")
		})
	}
}
