package logger

import (
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
func NewLogger(cfg Config) *slog.Logger {
	var handler slog.Handler
	var output io.Writer

	switch cfg.Output {
	case "stdout":
		output = os.Stdout
	case "stderr":
		output = os.Stderr
	default:
		output = os.Stdout // Default to stdout
	}

	level := new(slog.Level)
	if err := level.UnmarshalText([]byte(cfg.Level)); err != nil {
		level = new(slog.Level) // Default to Info if parsing fails
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
