package agent

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	goframeagent "github.com/sevigo/goframe/agent"
	"github.com/sevigo/goframe/llms"
)

// loopObserver implements goframeagent.AgentObserver to track token usage and
// tool calls across agent loop iterations. It replaces the previous TracingModel
// wrapper since goframe v0.37.0 now populates LoopResult.Tokens natively.
type loopObserver struct {
	logger    *slog.Logger
	sessionID string
	phase     string

	totalIn  float64
	totalOut float64
	calls    int
}

func newLoopObserver(logger *slog.Logger, sessionID, phase string) *loopObserver {
	return &loopObserver{
		logger:    logger,
		sessionID: sessionID,
		phase:     phase,
	}
}

func (o *loopObserver) OnIterationStart(_ context.Context, iteration int) {
	o.logger.Debug("agent loop iteration start",
		"session_id", o.sessionID, "phase", o.phase, "iteration", iteration)
}

func (o *loopObserver) OnThinkComplete(_ context.Context, _ string, _ []llms.ToolCall, tokens goframeagent.TokenUsage, _ error) {
	o.totalIn += tokens.Input
	o.totalOut += tokens.Output
	o.calls++
}

func (o *loopObserver) OnToolCall(_ context.Context, toolName string, _ map[string]any) {
	o.logger.Debug("agent tool call",
		"session_id", o.sessionID, "phase", o.phase, "tool", toolName)
}

func (o *loopObserver) OnToolResult(_ context.Context, toolName string, _ map[string]any, _ any, duration time.Duration, err error) {
	if err != nil {
		o.logger.Warn("agent tool error",
			"session_id", o.sessionID, "phase", o.phase, "tool", toolName,
			"duration", duration, "error", err)
	}
}

func (o *loopObserver) OnLoopComplete(_ context.Context, result *goframeagent.LoopResult, _ error) {
	toolSummary := make(map[string]int)
	for _, tc := range result.ToolCalls {
		toolSummary[tc.Name]++
	}

	o.logger.Info("agent loop complete",
		"session_id", o.sessionID, "phase", o.phase,
		"iterations", result.Iterations,
		"tokens_in", fmt.Sprintf("%.0f", o.totalIn),
		"tokens_out", fmt.Sprintf("%.0f", o.totalOut),
		"llm_calls", o.calls,
		"tool_calls", toolSummary,
		"result_tokens_in", fmt.Sprintf("%.0f", result.Tokens.Input),
		"result_tokens_out", fmt.Sprintf("%.0f", result.Tokens.Output),
	)
}
