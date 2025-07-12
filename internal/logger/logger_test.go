package logger_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/sevigo/code-warden/internal/logger"
	"github.com/stretchr/testify/assert"
)

func TestNewLogger(t *testing.T) {
	tests := []struct {
		name   string
		config logger.Config
	}{
		{
			name: "Text Logger Info Level",
			config: logger.Config{
				Level:  "info",
				Format: "text",
				Output: "stdout",
			},
		},
		{
			name: "JSON Logger Debug Level",
			config: logger.Config{
				Level:  "debug",
				Format: "json",
				Output: "stdout",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := logger.NewLogger(tt.config, &buf)
			slog.SetDefault(logger)

			if tt.config.Level == "debug" {
				slog.Debug("test message")
			} else {
				slog.Info("test message")
			}

			output := buf.String()

			switch tt.config.Format {
			case "text":
				assert.Contains(t, output, "level=INFO")
				assert.Contains(t, output, `msg="test message"`)
			case "json":
				var logEntry map[string]interface{}
				err := json.Unmarshal([]byte(output), &logEntry)
				assert.NoError(t, err, "output should be valid JSON")
				assert.Equal(t, "DEBUG", logEntry["level"])
				assert.Equal(t, "test message", logEntry["msg"])
			}
		})
	}
}
