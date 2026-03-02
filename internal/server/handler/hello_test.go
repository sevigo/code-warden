package handler_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/sevigo/code-warden/internal/server/handler"
)

func TestHelloHandler(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/hello", nil)
	rec := httptest.NewRecorder()

	handler.HelloHandler(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var response handler.HelloResponse
	err := json.NewDecoder(rec.Body).Decode(&response)
	assert.NoError(t, err)

	assert.Equal(t, "Hello from Code-Warden!", response.Message)
	assert.NotEmpty(t, response.Timestamp)

	_, parseErr := time.Parse(time.RFC3339, response.Timestamp)
	assert.NoError(t, parseErr, "timestamp should be valid RFC3339 format")
}
