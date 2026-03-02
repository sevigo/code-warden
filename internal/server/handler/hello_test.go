package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHelloHandler_Handle(t *testing.T) {
	handler := NewHelloHandler()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/hello", nil)
	recorder := httptest.NewRecorder()

	handler.Handle(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, recorder.Code)
	}

	var response HelloResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if response.Message != "Hello from Code-Warden!" {
		t.Errorf("expected message %q, got %q", "Hello from Code-Warden!", response.Message)
	}

	if _, err := time.Parse(time.RFC3339, response.Timestamp); err != nil {
		t.Errorf("timestamp is not in ISO8601 format: %v", err)
	}
}
