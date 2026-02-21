//go:build wireinject
// +build wireinject

package wire

import (
	"bytes"
	"os"
	"testing"
	"time"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/logger"
)

func TestProvideLogWriter(t *testing.T) {
	tests := []struct {
		name           string
		loggingOutput  string
		expectedStdout bool
		expectedStderr bool
		expectedFile   bool
	}{
		{
			name:           "stdout output",
			loggingOutput:  "stdout",
			expectedStdout: true,
			expectedStderr: false,
			expectedFile:   false,
		},
		{
			name:           "stderr output",
			loggingOutput:  "stderr",
			expectedStdout: false,
			expectedStderr: true,
			expectedFile:   false,
		},
		{
			name:           "file output",
			loggingOutput:  "file",
			expectedStdout: false,
			expectedStderr: false,
			expectedFile:   true,
		},
		{
			name:           "default output",
			loggingOutput:  "",
			expectedStdout: true,
			expectedStderr: false,
			expectedFile:   false,
		},
		{
			name:           "invalid output falls back to stdout",
			loggingOutput:  "invalid",
			expectedStdout: true,
			expectedStderr: false,
			expectedFile:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Logging: logger.Config{
					Output: tt.loggingOutput,
				},
			}

			writer := provideLogWriter(cfg)

			switch {
			case tt.expectedStdout:
				if writer != os.Stdout {
					t.Errorf("expected stdout, got %T", writer)
				}
			case tt.expectedStderr:
				if writer != os.Stderr {
					t.Errorf("expected stderr, got %T", writer)
				}
			case tt.expectedFile:
				// For file output, verify it's not stdout/stderr
				if writer == os.Stdout || writer == os.Stderr {
					t.Errorf("expected file writer, got stdio")
				}
				// Clean up the test log file if created
				if f, ok := writer.(*os.File); ok {
					f.Close()
					os.Remove("code-warden.log")
				}
			}
		})
	}
}

func TestProvideLogWriter_FileErrorFallback(t *testing.T) {
	// Create a directory with the same name to cause OpenFile to fail
	cfg := &config.Config{
		Logging: logger.Config{
			Output: "file",
		},
	}

	// Create a directory with the log file name to cause an error
	os.Mkdir("code-warden.log", 0755)
	defer os.RemoveAll("code-warden.log")

	// Capture stderr to verify error message
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	writer := provideLogWriter(cfg)

	w.Close()
	os.Stderr = oldStderr

	var buf bytes.Buffer
	buf.ReadFrom(r)

	// Should fall back to stdout
	if writer != os.Stdout {
		t.Errorf("expected stdout fallback on error, got %T", writer)
	}

	// Should have logged error message
	errOutput := buf.String()
	if !bytes.Contains([]byte(errOutput), []byte("failed to open log file")) {
		t.Logf("stderr output: %s", errOutput)
	}
}

func TestProvideLoggerConfig(t *testing.T) {
	cfg := &config.Config{
		Logging: logger.Config{
			Level:  "debug",
			Output: "stdout",
		},
	}

	result := provideLoggerConfig(cfg)

	if result.Level != "debug" {
		t.Errorf("expected level debug, got %s", result.Level)
	}
	if result.Output != "stdout" {
		t.Errorf("expected output stdout, got %s", result.Output)
	}
}

func TestProvideDBConfig(t *testing.T) {
	cfg := &config.Config{
		Database: config.DBConfig{
			Host:     "localhost",
			Port:     5432,
			Database: "testdb",
		},
	}

	result := provideDBConfig(cfg)

	if result.Host != "localhost" {
		t.Errorf("expected host localhost, got %s", result.Host)
	}
	if result.Port != 5432 {
		t.Errorf("expected port 5432, got %d", result.Port)
	}
	if result.Database != "testdb" {
		t.Errorf("expected database testdb, got %s", result.Database)
	}
}

func TestProvideSlogLogger(t *testing.T) {
	config := logger.Config{
		Level:  "info",
		Output: "stdout",
	}

	logger := provideSlogLogger(config, os.Stdout)

	if logger == nil {
		t.Error("expected non-nil logger")
	}
}

func TestNewOllamaHTTPClient(t *testing.T) {
	client := newOllamaHTTPClient()

	if client == nil {
		t.Error("expected non-nil HTTP client")
	}

	// Verify timeout is set correctly (15 minutes)
	expectedTimeout := 15 * time.Minute
	if client.Timeout != expectedTimeout {
		t.Errorf("expected timeout %v, got %v", expectedTimeout, client.Timeout)
	}

	// Verify transport is configured
	if client.Transport == nil {
		t.Error("expected non-nil transport")
	}
}
