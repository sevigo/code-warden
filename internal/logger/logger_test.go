package logger

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestNewLogger(t *testing.T) {
	tests := []struct {
		name           string
		config         Config
		expectedOutput string
		checkFunc      func(t *testing.T, output string)
	}{
		{
			name: "Text Logger Info Level",
			config: Config{
				Level:  "info",
				Format: "text",
				Output: "stdout",
			},
			checkFunc: func(t *testing.T, output string) {
				if !bytes.Contains([]byte(output), []byte("level=INFO")) ||
					!bytes.Contains([]byte(output), []byte("msg=\"test message\"")) {
					t.Errorf("Expected text log output with info level and message, got: %s", output)
				}
			},
		},
		{
			name: "JSON Logger Debug Level",
			config: Config{
				Level:  "debug",
				Format: "json",
				Output: "stdout",
			},
			checkFunc: func(t *testing.T, output string) {
				var logEntry map[string]interface{}
				err := json.Unmarshal([]byte(output), &logEntry)
				if err != nil {
					t.Fatalf("Failed to unmarshal JSON log: %v, output: %s", err, output)
				}
				if logEntry["level"] != "DEBUG" || logEntry["msg"] != "test message" {
					t.Errorf("Expected JSON log output with debug level and message, got: %v", logEntry)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := NewLogger(tt.config, &buf)
			slog.SetDefault(logger)

			if tt.config.Level == "debug" {
				slog.Debug("test message")
			} else {
				slog.Info("test message")
			}

			tt.checkFunc(t, buf.String())
		})
	}
}
