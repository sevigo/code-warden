package logger

import (
	"fmt"
	"io"
	"log/slog"
	"os"
)

// Config holds the logger configuration.
type Config struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
	Output string `mapstructure:"output"`
}

// NewLogger initializes a new slog logger based on the provided configuration.
func NewLogger(cfg Config, output io.Writer) *slog.Logger {
	var handler slog.Handler

	if output == nil {
		switch cfg.Output {
		case "stdout":
			output = os.Stdout
		case "stderr":
			output = os.Stderr
		case "file":
			file, err := os.OpenFile("app.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
			if err != nil {
				fmt.Printf("Failed to open log file: %v\n", err)
				output = os.Stdout
			} else {
				output = file
			}
		default:
			output = os.Stdout
		}
	}

	level := new(slog.Level)
	if err := level.UnmarshalText([]byte(cfg.Level)); err != nil {
		level = new(slog.Level)
	}

	switch cfg.Format {
	case "json":
		handler = slog.NewJSONHandler(output, &slog.HandlerOptions{
			Level: level,
		})
	case "text":
		fallthrough
	default:
		handler = slog.NewTextHandler(output, &slog.HandlerOptions{
			Level: level,
		})
	}

	return slog.New(handler)
}
