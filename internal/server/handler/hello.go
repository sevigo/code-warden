package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

type HelloResponse struct {
	Message   string `json:"message"`
	Timestamp string `json:"timestamp"`
}

type HelloHandler struct {
	logger *slog.Logger
}

func NewHelloHandler(logger *slog.Logger) *HelloHandler {
	return &HelloHandler{
		logger: logger,
	}
}

func (h *HelloHandler) Handle(_ context.Context, w http.ResponseWriter, _ *http.Request) {
	response := HelloResponse{
		Message:   "Hello from Code-Warden!",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(response); err != nil {
		if h.logger != nil {
			h.logger.Error("failed to encode hello response", "error", err)
		}
	}
}
