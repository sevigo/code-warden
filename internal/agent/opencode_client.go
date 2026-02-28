// Package agent provides the OpenCode client for interacting with the OpenCode server.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// safeBranchNameRegex validates that branch names contain only safe characters.
var safeBranchNameRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/-]*$`)

// shellQuote safely quotes a string for shell execution by wrapping in single quotes
// and escaping any existing single quotes.
func shellQuote(s string) string {
	// Replace single quotes with '\'' (end quote, escaped quote, start quote)
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// validateBranchName checks if a branch name is safe for shell execution.
func validateBranchName(name string) error {
	if name == "" {
		return fmt.Errorf("branch name cannot be empty")
	}
	if !safeBranchNameRegex.MatchString(name) {
		return fmt.Errorf("invalid branch name: contains unsafe characters")
	}
	return nil
}

// OpenCodeClient handles communication with the OpenCode HTTP API.
type OpenCodeClient struct {
	baseURL    string
	password   string
	httpClient *http.Client
	logger     *slog.Logger
}

// NewOpenCodeClient creates a new OpenCode API client.
func NewOpenCodeClient(baseURL, password string, logger *slog.Logger) *OpenCodeClient {
	if baseURL != "" && !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		baseURL = "http://" + baseURL
	}

	return &OpenCodeClient{
		baseURL:  baseURL,
		password: password,
		logger:   logger,
		httpClient: &http.Client{
			Timeout: 30 * time.Minute, // Match the session timeout for LLM operations
		},
	}
}

// OpenCodeSession represents an OpenCode session.
type OpenCodeSession struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

// Message represents a message in a session.
type Message struct {
	ID        string `json:"id"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
}

// CreateSessionRequest is the body for creating a session.
type CreateSessionRequest struct {
	Title string            `json:"title"`
	Model string            `json:"model,omitempty"`
	Env   map[string]string `json:"env,omitempty"`
}

// Part represents a content part in a message.
type Part struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// ModelConfig specifies the model and provider for a request.
type ModelConfig struct {
	ProviderID string `json:"providerID"`
	ModelID    string `json:"modelID"`
}

// SendMessageRequest is the body for sending a message.
type SendMessageRequest struct {
	Agent  string           `json:"agent,omitempty"`
	Model  *ModelConfig     `json:"model,omitempty"`
	Parts  []Part           `json:"parts"`
	Tools  []ToolDefinition `json:"tools,omitempty"`
	Stream bool             `json:"stream,omitempty"`
}

// ToolDefinition defines a tool for the agent.
type ToolDefinition struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// SendMessageResponse is the response from sending a message.
type SendMessageResponse struct {
	Info  Message `json:"info"`
	Parts []Part  `json:"parts"`
}

// ShellRequest is the body for executing a shell command.
type ShellRequest struct {
	Agent   string       `json:"agent"`
	Model   *ModelConfig `json:"model,omitempty"`
	Command string       `json:"command"`
}

// doRequest performs an HTTP request with authentication.
func (c *OpenCodeClient) doRequest(ctx context.Context, method, path string, body interface{}) ([]byte, error) {
	var reqBody io.Reader
	if body != nil {
		jsonData, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(jsonData)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.password)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode >= 400 {
		c.logger.Error("API request failed", "method", method, "path", path, "status", resp.StatusCode, "response", string(respBody))
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	c.logger.Debug("API response received", "method", method, "path", path, "status", resp.StatusCode, "size", len(respBody))
	return respBody, nil
}

// CreateSession creates a new OpenCode session.
func (c *OpenCodeClient) CreateSession(ctx context.Context, title, model string, env map[string]string) (*OpenCodeSession, error) {
	req := CreateSessionRequest{
		Title: title,
		Model: model,
		Env:   env,
	}

	respBody, err := c.doRequest(ctx, http.MethodPost, "/session", req)
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}

	var session OpenCodeSession
	if err := json.Unmarshal(respBody, &session); err != nil {
		return nil, fmt.Errorf("failed to parse session response: %w", err)
	}

	return &session, nil
}

// SendMessage sends a message to a session and returns the response.
func (c *OpenCodeClient) SendMessage(ctx context.Context, sessionID string, content string, tools []ToolDefinition) (*SendMessageResponse, error) {
	req := SendMessageRequest{
		Agent:  "build", // Use the "build" agent per curl defaults
		Parts:  []Part{{Type: "text", Text: content}},
		Tools:  tools,
		Stream: false,
	}

	respBody, err := c.doRequest(ctx, http.MethodPost, "/session/"+sessionID+"/message", req)
	if err != nil {
		return nil, fmt.Errorf("failed to send message: %w", err)
	}

	c.logger.Debug("Raw SendMessage response", "response", string(respBody))

	var response SendMessageResponse
	if len(respBody) > 0 {
		if err := json.Unmarshal(respBody, &response); err != nil {
			return nil, fmt.Errorf("failed to parse message response: %w", err)
		}
	}

	return &response, nil
}

// GetSession retrieves session information.
func (c *OpenCodeClient) GetSession(ctx context.Context, sessionID string) (*OpenCodeSession, error) {
	respBody, err := c.doRequest(ctx, http.MethodGet, "/session/"+sessionID, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get session: %w", err)
	}

	var session OpenCodeSession
	if err := json.Unmarshal(respBody, &session); err != nil {
		return nil, fmt.Errorf("failed to parse session response: %w", err)
	}

	return &session, nil
}

// GetMessages retrieves all messages in a session.
func (c *OpenCodeClient) GetMessages(ctx context.Context, sessionID string) ([]Message, error) {
	respBody, err := c.doRequest(ctx, http.MethodGet, "/session/"+sessionID+"/message", nil) // Use /message instead of /messages as per spec
	if err != nil {
		return nil, fmt.Errorf("failed to get messages: %w", err)
	}

	var messages []Message
	if err := json.Unmarshal(respBody, &messages); err != nil {
		return nil, fmt.Errorf("failed to parse messages response: %w", err)
	}

	return messages, nil
}

// ExecuteCommand runs a shell command in the session context.
func (c *OpenCodeClient) ExecuteCommand(ctx context.Context, sessionID, command string) (*SendMessageResponse, error) {
	req := ShellRequest{
		Agent:   "build",
		Command: command,
	}

	respBody, err := c.doRequest(ctx, http.MethodPost, "/session/"+sessionID+"/shell", req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute shell command: %w", err)
	}

	var response SendMessageResponse
	if len(respBody) > 0 {
		if err := json.Unmarshal(respBody, &response); err != nil {
			return nil, fmt.Errorf("failed to parse shell response: %w", err)
		}
	}

	return &response, nil
}

// CreateBranch creates a new git branch.
func (c *OpenCodeClient) CreateBranch(ctx context.Context, sessionID, branchName string) error {
	if err := validateBranchName(branchName); err != nil {
		return fmt.Errorf("invalid branch name: %w", err)
	}
	_, err := c.ExecuteCommand(ctx, sessionID, "git checkout -b "+shellQuote(branchName))
	return err
}

// CommitChanges commits all staged changes.
func (c *OpenCodeClient) CommitChanges(ctx context.Context, sessionID, message string) error {
	// Stage all changes first
	if _, err := c.ExecuteCommand(ctx, sessionID, "git add -A"); err != nil {
		return fmt.Errorf("failed to stage changes: %w", err)
	}

	// Commit with properly escaped message
	_, err := c.ExecuteCommand(ctx, sessionID, "git commit -m "+shellQuote(message))
	return err
}

// PushBranch pushes the current branch to remote.
func (c *OpenCodeClient) PushBranch(ctx context.Context, sessionID, branchName string) error {
	if err := validateBranchName(branchName); err != nil {
		return fmt.Errorf("invalid branch name: %w", err)
	}
	_, err := c.ExecuteCommand(ctx, sessionID, "git push -u origin "+shellQuote(branchName))
	return err
}

// CreatePullRequest creates a PR via gh CLI and returns the PR URL and number.
func (c *OpenCodeClient) CreatePullRequest(ctx context.Context, sessionID, title, body string) (string, int, error) {
	// Use --json flag to get structured output
	cmd := fmt.Sprintf("gh pr create --title %q --body %q --json url,number", title, body)
	resp, err := c.ExecuteCommand(ctx, sessionID, cmd)
	if err != nil {
		return "", 0, fmt.Errorf("failed to create PR: %w", err)
	}

	// Parse the JSON response from gh
	var prResult struct {
		URL    string `json:"url"`
		Number int    `json:"number"`
	}
	// The response comes as Parts from ExecuteCommand, we need to extract the JSON
	// For now, we parse the text content
	for _, part := range resp.Parts {
		if part.Type == "text" {
			if err := json.Unmarshal([]byte(part.Text), &prResult); err != nil {
				c.logger.Warn("failed to parse PR response, returning default", "error", err)
				return "PR created (URL not parsed)", 0, nil
			}
			return prResult.URL, prResult.Number, nil
		}
	}

	return "PR created successfully", 0, nil
}

// CloseSession closes a session.
func (c *OpenCodeClient) CloseSession(ctx context.Context, sessionID string) error {
	_, err := c.doRequest(ctx, http.MethodDelete, "/session/"+sessionID, nil)
	return err
}

// HealthCheck checks if the server is ready.
func (c *OpenCodeClient) HealthCheck(ctx context.Context) error {
	_, err := c.doRequest(ctx, http.MethodGet, "/global/health", nil)
	return err
}
