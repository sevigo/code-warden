package review

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"time"

	goframeagent "github.com/sevigo/goframe/agent"
	"github.com/sevigo/goframe/llms"
)

// reviewObserver emits progress logs for a single angle pass so users can see
// what the review agent is investigating in real time. It implements the
// goframe agent.AgentObserver interface.
type reviewObserver struct {
	logger *slog.Logger
	angle  string
}

// newReviewObserver returns an observer that logs tool activity for one angle.
func newReviewObserver(logger *slog.Logger, angle string) *reviewObserver {
	return &reviewObserver{logger: logger, angle: angle}
}

// OnIterationStart logs the start of each think-act-observe cycle.
func (o *reviewObserver) OnIterationStart(_ context.Context, iteration int) {
	o.logger.Info("review: angle iteration",
		"angle", o.angle,
		"iteration", iteration,
	)
}

// OnThinkComplete logs that the model finished reasoning for this cycle.
func (o *reviewObserver) OnThinkComplete(_ context.Context, _ string, toolCalls []llms.ToolCall, _ goframeagent.TokenUsage, err error) {
	o.logger.Info("review: angle thought complete",
		"angle", o.angle,
		"tool_calls", len(toolCalls),
		"error", err,
	)
}

// OnToolCall logs an investigation step (e.g. grep/read_file) the agent performs.
func (o *reviewObserver) OnToolCall(_ context.Context, toolName string, params map[string]any) {
	arg := summarizeParams(params)
	o.logger.Info("review: angle tool call",
		"angle", o.angle,
		"tool", toolName,
		"arg", arg,
	)
}

// OnToolResult logs the outcome and duration of a tool invocation.
func (o *reviewObserver) OnToolResult(_ context.Context, toolName string, _ map[string]any, result any, duration time.Duration, err error) {
	if err != nil {
		o.logger.Warn("review: angle tool error",
			"angle", o.angle,
			"tool", toolName,
			"duration", duration.String(),
			"error", err,
		)
		return
	}
	o.logger.Info("review: angle tool result",
		"angle", o.angle,
		"tool", toolName,
		"duration", duration.String(),
		"result", summarizeResult(result),
	)
}

// OnLoopComplete logs the final outcome of the angle's agent loop.
func (o *reviewObserver) OnLoopComplete(_ context.Context, result *goframeagent.LoopResult, err error) {
	if result == nil {
		o.logger.Warn("review: angle complete",
			"angle", o.angle,
			"error", err,
		)
		return
	}
	o.logger.Info("review: angle complete",
		"angle", o.angle,
		"iterations", result.Iterations,
		"tokens_in", result.Tokens.Input,
		"tokens_out", result.Tokens.Output,
		"error", err,
	)
}

// summarizeParams extracts a short, human-readable argument from tool params.
func summarizeParams(params map[string]any) string {
	// Tools like grep/read_file/find/list_dir accept a path or pattern.
	for _, key := range []string{"pattern", "path", "query"} {
		if v, ok := params[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// summarizeResult produces a compact description of a tool result.
func summarizeResult(result any) string {
	if result == nil {
		return ""
	}
	// The grep/find/read_file tools return map[string]any.
	if m, ok := result.(map[string]any); ok {
		if n, ok := m["count"]; ok {
			return jsonInt(n)
		}
		if s, ok := m["output"].(string); ok {
			return truncate(s, 60)
		}
		if s, ok := m["content"].(string); ok {
			return truncate(s, 60)
		}
	}
	// Fallback: JSON marshal, truncated.
	b, err := json.Marshal(result)
	if err != nil {
		return ""
	}
	return truncate(string(b), 80)
}

// jsonInt formats a count-like value from a tool result map.
func jsonInt(v any) string {
	switch n := v.(type) {
	case int:
		return strconv.Itoa(n)
	case float64:
		return strconv.Itoa(int(n))
	default:
		return ""
	}
}

// truncate shortens a string to maxLen bytes, appending an ellipsis.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

var _ goframeagent.AgentObserver = (*reviewObserver)(nil)
