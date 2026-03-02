package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHello(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/hello", nil)
	rec := httptest.NewRecorder()

	Hello(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var response HelloResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	expectedMessage := "Hello from Code-Warden!"
	if response.Message != expectedMessage {
		t.Errorf("expected message %q, got %q", expectedMessage, response.Message)
	}

	if response.Timestamp == "" {
		t.Error("timestamp should not be empty")
	}

	_, err := time.Parse(time.RFC3339, response.Timestamp)
	if err != nil {
		t.Errorf("timestamp is not in ISO8601 format: %v", err)
	}
}
