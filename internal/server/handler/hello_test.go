package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHelloHandler(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/hello", nil)
	rec := httptest.NewRecorder()

	HelloHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var resp HelloResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Message != "Hello from Code-Warden!" {
		t.Errorf("expected message %q, got %q", "Hello from Code-Warden!", resp.Message)
	}

	if _, err := time.Parse(time.RFC3339, resp.Timestamp); err != nil {
		t.Errorf("invalid timestamp format: %v", err)
	}
}
