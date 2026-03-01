package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestShellQuote(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"safe", "'safe'"},
		{"has space", "'has space'"},
		{"has 'quote'", "'has '\\''quote'\\'''"},
		{"", "''"},
	}

	for _, tt := range tests {
		if got := shellQuote(tt.input); got != tt.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestValidateBranchName(t *testing.T) {
	tests := []struct {
		name    string
		branch  string
		wantErr bool
	}{
		{"valid simple", "feature/tests", false},
		{"valid with dots", "fix/v1.2", false},
		{"empty", "", true},
		{"too long", string(make([]byte, 300)), true},
		{"unsafe chars", "feature; rm -rf /", true},
		{"consecutive dots", "feature..fix", true},
		{"starts with hyphen", "-feature", true},
		{"safe chars with -", "feature-branch", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateBranchName(tt.branch)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateBranchName(%q) error = %v, wantErr %v", tt.branch, err, tt.wantErr)
			}
		})
	}
}

func TestOpenCodeClient_CreateSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/session" {
			t.Errorf("expected POST /session, got %s %s", r.Method, r.URL.Path)
		}

		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-pass" {
			t.Errorf("expected Bearer test-pass, got %s", auth)
		}

		resp := OpenCodeSession{
			ID:     "sess-123",
			Status: "pending",
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewOpenCodeClient(server.URL, "test-pass", slog.New(slog.NewTextHandler(os.Stderr, nil)))
	session, err := client.CreateSession(context.Background(), "Test", "model", nil)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	if session.ID != "sess-123" {
		t.Errorf("expected session ID sess-123, got %s", session.ID)
	}
}

func TestOpenCodeClient_SendMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/session/sess-123/message" {
			t.Errorf("expected POST /session/sess-123/message, got %s %s", r.Method, r.URL.Path)
		}

		var req SendMessageRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.Parts[0].Text != "hello" {
			t.Errorf("expected content hello, got %s", req.Parts[0].Text)
		}

		resp := SendMessageResponse{
			Info:  Message{ID: "msg-1"},
			Parts: []Part{{Type: "text", Text: "world"}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewOpenCodeClient(server.URL, "pass", slog.Default())
	resp, err := client.SendMessage(context.Background(), "sess-123", "hello", nil)
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	if resp.Parts[0].Text != "world" {
		t.Errorf("expected response world, got %s", resp.Parts[0].Text)
	}
}

func TestOpenCodeClient_CreatePullRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := SendMessageResponse{
			Parts: []Part{{
				Type: "text",
				Text: `{"url": "https://github.com/pr/1", "number": 1}`,
			}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewOpenCodeClient(server.URL, "pass", slog.Default())
	url, num, err := client.CreatePullRequest(context.Background(), "sess-123", "Title", "Body")
	if err != nil {
		t.Fatalf("CreatePullRequest failed: %v", err)
	}

	if url != "https://github.com/pr/1" || num != 1 {
		t.Errorf("expected url and 1, got %s and %d", url, num)
	}
}

func TestOpenCodeClient_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"type": "error", "code": "invalid_request", "message": "bad things"}`))
	}))
	defer server.Close()

	client := NewOpenCodeClient(server.URL, "pass", slog.Default())
	_, err := client.GetSession(context.Background(), "sess-123")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.Code != "invalid_request" {
		t.Errorf("expected code invalid_request, got %s", apiErr.Code)
	}
}
