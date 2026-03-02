package handler

import (
	"encoding/json"
	"net/http"
	"time"
)

type HelloHandler struct{}

func NewHelloHandler() *HelloHandler {
	return &HelloHandler{}
}

type HelloResponse struct {
	Message   string `json:"message"`
	Timestamp string `json:"timestamp"`
}

func (h *HelloHandler) Handle(w http.ResponseWriter, _ *http.Request) {
	response := HelloResponse{
		Message:   "Hello from Code-Warden!",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(response)
}
