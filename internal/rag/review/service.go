package review

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/sevigo/goframe/llms"

	"github.com/sevigo/code-warden/internal/core"
	internalgithub "github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/llm"
	"github.com/sevigo/code-warden/internal/storage"
)

// ContextBuilderFunc generates the context needed for code reviews.
type ContextBuilderFunc func(ctx context.Context, collectionName, embedderModelName, repoPath string, changedFiles []internalgithub.ChangedFile, prContext string) (string, string)

// LLMFactory returns an LLM instance for a given model name.
type LLMFactory func(ctx context.Context, modelName string) (llms.Model, error)

// Config holds dependencies for the Service.
type Config struct {
	VectorStore      storage.VectorStore
	PromptMgr        *llm.PromptManager
	GeneratorLLM     llms.Model
	GetLLM           LLMFactory
	Logger           *slog.Logger
	ConsensusTimeout string
	ConsensusQuorum  float64
	BuildContext     ContextBuilderFunc
}

// Service orchestrates code review generation.
type Service struct {
	cfg Config
}

// NewService creates a new [Service] instance.
func NewService(cfg Config) *Service {
	return &Service{cfg: cfg}
}

// formatChangedFiles returns a markdown-formatted list of changed file paths.
func formatChangedFiles(files []internalgithub.ChangedFile) string {
	var builder strings.Builder
	for _, file := range files {
		builder.WriteString(fmt.Sprintf("- `%s`\n", file.Filename))
	}
	return builder.String()
}

// contextIsEmpty checks if both context strings are empty.
// This helps detect high hallucination risk.
func contextIsEmpty(contextString, definitionsContext string) bool {
	return contextString == "" && definitionsContext == ""
}

// getConsensusTimeout returns the timeout for individual model reviews in consensus mode.
// Falls back to 5 minutes if not configured or invalid.
func (s *Service) getConsensusTimeout() time.Duration {
	const defaultTimeout = 5 * time.Minute
	if s.cfg.ConsensusTimeout == "" {
		return defaultTimeout
	}
	d, err := time.ParseDuration(s.cfg.ConsensusTimeout)
	if err != nil {
		s.cfg.Logger.Warn("invalid consensus_timeout config, using default", "error", err, "default", defaultTimeout)
		return defaultTimeout
	}
	return d
}

// buildReviewPromptData populates the template variables for prompt generation.
func (s *Service) buildReviewPromptData(event *core.GitHubEvent, repoConfig *core.RepoConfig, contextString, definitionsContext, diff string, changedFiles []internalgithub.ChangedFile) map[string]string {
	return map[string]string{
		"Title":              event.PRTitle,
		"Description":        event.PRBody,
		"Language":           event.Language,
		"CustomInstructions": strings.Join(repoConfig.CustomInstructions, "\n"),
		"ChangedFiles":       formatChangedFiles(changedFiles),
		"Context":            contextString,
		"Definitions":        definitionsContext,
		"Diff":               diff,
	}
}

// generateResponseWithPrompt renders a prompt template and calls the generator LLM.
func (s *Service) generateResponseWithPrompt(ctx context.Context, event *core.GitHubEvent, promptKey llm.PromptKey, promptData any) (string, error) {
	prompt, err := s.cfg.PromptMgr.Render(promptKey, promptData)
	if err != nil {
		return "", fmt.Errorf("could not render prompt '%s': %w", promptKey, err)
	}

	s.cfg.Logger.Info("calling LLM for response generation",
		"repo", event.RepoFullName,
		"pr", event.PRNumber,
		"prompt_key", promptKey,
	)

	response, err := s.cfg.GeneratorLLM.Call(ctx, prompt)
	if err != nil {
		return "", fmt.Errorf("LLM generation failed for prompt '%s': %w", promptKey, err)
	}

	s.cfg.Logger.Info("LLM response generated successfully", "chars", len(response))
	return response, nil
}
