package review

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	goframeagent "github.com/sevigo/goframe/agent"
	"github.com/sevigo/goframe/llms"
	"github.com/sevigo/goframe/schema"

	llmpkg "github.com/sevigo/code-warden/internal/llm"
)

// ToolBuilder constructs the read-only investigation tools wired to a
// workspace root.
type ToolBuilder func(workspaceRoot string) []goframeagent.Tool

// GoframeAngleExecutor executes one review angle with a goframe agent loop.
type GoframeAngleExecutor struct {
	llm       llms.Model
	promptMgr *llmpkg.PromptManager
	tools     ToolBuilder
	logger    *slog.Logger
}

// NewGoframeAngleExecutor creates the production angle executor.
func NewGoframeAngleExecutor(model llms.Model, promptMgr *llmpkg.PromptManager, tools ToolBuilder, logger *slog.Logger) *GoframeAngleExecutor {
	if logger == nil {
		logger = slog.Default()
	}
	return &GoframeAngleExecutor{
		llm:       model,
		promptMgr: promptMgr,
		tools:     tools,
		logger:    logger,
	}
}

// Execute runs a single agent pass and returns its findings and execution data.
func (e *GoframeAngleExecutor) Execute(ctx context.Context, request AngleRequest) (AngleResult, error) {
	angle := request.Angle
	e.logger.Info("agent review: starting angle", "angle", angle.Name, "workspace", request.Workspace)

	systemPrompt, err := e.promptMgr.Raw(angle.PromptKey)
	if err != nil {
		return AngleResult{}, fmt.Errorf("agent review angle %s: %w", angle.Name, err)
	}

	registry := goframeagent.NewRegistry()
	if e.tools != nil {
		for _, tool := range e.tools(request.Workspace) {
			if err := registry.Register(tool); err != nil {
				e.logger.Warn("agent review: failed to register tool",
					"angle", angle.Name, "tool", tool.Name(), "error", err)
			}
		}
	}
	getDiff := newGetDiffTool(request.Diff, request.ChangedLines, request.FilenameIndex)
	if err := registry.Register(getDiff); err != nil {
		e.logger.Warn("agent review: failed to register get_diff tool",
			"angle", angle.Name, "error", err)
	}

	loop, err := goframeagent.NewAgentLoop(e.llm, registry,
		goframeagent.WithLoopSystemPrompt(systemPrompt),
		goframeagent.WithLoopMaxIterations(request.MaxIterations),
		goframeagent.WithLoopObserver(newReviewObserver(e.logger, angle.Name)),
		goframeagent.WithLoopCompactionHook(e.compactionHook(angle.Name, request.ContextWindow)),
	)
	if err != nil {
		return AngleResult{}, fmt.Errorf("agent review angle %s: new loop: %w", angle.Name, err)
	}

	task := goframeagent.Task{
		ID:          angle.Name,
		Description: fmt.Sprintf("Review the PR for %s", angle.Description),
		Context:     request.TaskContext,
	}

	loopResult, runErr := loop.Run(ctx, task, nil)
	if runErr != nil && !errors.Is(runErr, goframeagent.ErrMaxIterations) {
		return AngleResult{}, fmt.Errorf("agent review angle %s: loop failed: %w", angle.Name, runErr)
	}
	status := AngleStatusCompleted
	if runErr != nil {
		status = AngleStatusPartial
		e.logger.Warn("agent review: angle hit iteration cap, parsing partial response",
			"angle", angle.Name, "error", runErr)
	}
	if loopResult == nil {
		return AngleResult{}, fmt.Errorf("agent review angle %s: loop returned no result", angle.Name)
	}

	if loopResult.Response != "" {
		e.logger.Info("agent review: angle raw response",
			"angle", angle.Name,
			"response", loopResult.Response,
		)
	}

	parser := NewStructuredReviewParser(e.logger)
	structuredReview, err := parser.Parse(ctx, loopResult.Response)
	if err != nil {
		return AngleResult{}, fmt.Errorf("agent review angle %s: parse response: %w", angle.Name, err)
	}
	if structuredReview == nil {
		return AngleResult{}, fmt.Errorf("agent review angle %s: parser returned no review", angle.Name)
	}

	return AngleResult{
		Angle:       angle.Name,
		Suggestions: structuredReview.Suggestions,
		Raw:         loopResult.Response,
		Iterations:  loopResult.Iterations,
		TokensIn:    int(loopResult.Tokens.Input),
		TokensOut:   int(loopResult.Tokens.Output),
		Status:      status,
	}, nil
}

// compactionHook compacts accumulated tool history while retaining the system
// prompt, initial task, and most recent tool results.
func (e *GoframeAngleExecutor) compactionHook(angle string, contextWindow int) func(context.Context, []schema.MessageContent, goframeagent.TokenUsage) []schema.MessageContent {
	compactAtTokens := int(float64(contextWindow) * 0.6)
	return func(_ context.Context, msgs []schema.MessageContent, tokens goframeagent.TokenUsage) []schema.MessageContent {
		if int(tokens.Input) < compactAtTokens {
			return nil
		}
		e.logger.Info("agent review: compacting context",
			"angle", angle,
			"tokens_in", int(tokens.Input),
			"context_window", contextWindow,
			"threshold", compactAtTokens,
			"messages_before", len(msgs),
		)
		compacted := make([]schema.MessageContent, 0, 6)
		for _, message := range msgs {
			if message.Role == schema.ChatMessageTypeSystem {
				compacted = append(compacted, message)
				break
			}
		}
		for _, message := range msgs {
			if message.Role == schema.ChatMessageTypeHuman {
				compacted = append(compacted, message)
				break
			}
		}
		compacted = append(compacted, schema.NewHumanMessage(
			"[Context compacted: earlier tool results were summarized to save space. "+
				"Call get_diff to re-fetch the diff if needed. Emit your <review> now if you have enough evidence.]",
		))
		var tail []schema.MessageContent
		for i := len(msgs) - 1; i >= 0 && len(tail) < 2; i-- {
			if msgs[i].Role == schema.ChatMessageTypeTool {
				tail = append(tail, msgs[i])
			}
		}
		for i := len(tail) - 1; i >= 0; i-- {
			compacted = append(compacted, tail[i])
		}
		return compacted
	}
}

var _ AngleExecutor = (*GoframeAngleExecutor)(nil)
