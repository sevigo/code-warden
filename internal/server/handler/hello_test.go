package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestHelloHandler(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/hello", nil)
	rec := httptest.NewRecorder()

	HelloHandler(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var response HelloResponse
	err := json.NewDecoder(rec.Body).Decode(&response)
	assert.NoError(t, err)
	assert.Equal(t, "Hello from Code-Warden!", response.Message)

	_, parseErr := time.Parse(time.RFC3339, response.Timestamp)
	assert.NoError(t, parseErr, "timestamp should be in RFC3339 format")
}
