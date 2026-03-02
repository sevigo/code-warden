package handler

import (
	"encoding/json"
	"net/http"
	"time"
)

type HelloResponse struct {
	Message   string `json:"message"`
	Timestamp string `json:"timestamp"`
}

func Hello(w http.ResponseWriter, _ *http.Request) {
	response := HelloResponse{
		Message:   "Hello from Code-Warden!",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
}
