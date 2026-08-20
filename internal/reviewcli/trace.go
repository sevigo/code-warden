package reviewcli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	agentreview "github.com/sevigo/code-warden/internal/agent/review"
	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/reviewapp"
)

const traceSchemaVersion = 1

// TraceModel identifies the model used for a review without containing
// provider credentials or endpoint configuration.
type TraceModel struct {
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	ThinkingEnabled bool   `json:"thinking_enabled"`
	ThinkingEffort  string `json:"thinking_effort,omitempty"`
}

// TraceRequest contains the safe inputs and outputs persisted for one local
// review run. Clone URLs and provider configuration are intentionally absent.
type TraceRequest struct {
	StartedAt  time.Time
	FinishedAt time.Time
	Model      TraceModel
	Input      reviewapp.ReviewInput
	Options    reviewapp.ReviewOptions
	Result     *reviewapp.ReviewResult
	Err        error
}

type traceManifest struct {
	SchemaVersion  int                          `json:"schema_version"`
	Status         string                       `json:"status"`
	StartedAt      time.Time                    `json:"started_at"`
	FinishedAt     time.Time                    `json:"finished_at"`
	DurationMillis int64                        `json:"duration_ms"`
	Repository     string                       `json:"repository"`
	Model          TraceModel                   `json:"model"`
	Options        traceOptions                 `json:"options"`
	ChangedFiles   []string                     `json:"changed_files"`
	CommitMessages []string                     `json:"commit_messages,omitempty"`
	DiffFile       string                       `json:"diff_file"`
	ReviewJSONFile string                       `json:"review_json_file,omitempty"`
	ReviewXMLFile  string                       `json:"review_xml_file,omitempty"`
	Angles         []traceAngleResult           `json:"angles,omitempty"`
	Coverage       *agentreview.CoverageReceipt `json:"coverage,omitempty"`
	Error          string                       `json:"error,omitempty"`
}

type traceOptions struct {
	AngleTimeoutMillis int64               `json:"angle_timeout_ms"`
	MaxIterations      int                 `json:"max_iterations"`
	ContextWindow      int                 `json:"context_window"`
	Review             *agentreview.Config `json:"review,omitempty"`
}

type traceAngleResult struct {
	Angle       string                  `json:"angle"`
	Status      agentreview.AngleStatus `json:"status"`
	Iterations  int                     `json:"iterations"`
	TokensIn    int                     `json:"tokens_in"`
	TokensOut   int                     `json:"tokens_out"`
	Suggestions []core.Suggestion       `json:"suggestions"`
	RawFile     string                  `json:"raw_file"`
}

// WriteTrace creates a private run directory beneath root and persists the
// review inputs, outputs, and per-angle execution data. The returned path is
// the newly created run directory.
func WriteTrace(root string, request TraceRequest) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("trace directory is required")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("create trace root: %w", err)
	}

	prefix := request.StartedAt.UTC().Format("20060102T150405Z") + "-"
	runDir, err := os.MkdirTemp(root, prefix)
	if err != nil {
		return "", fmt.Errorf("create trace run directory: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(runDir)
		}
	}()

	const diffFilename = "input.diff"
	if err := writeTraceFile(runDir, diffFilename, []byte(request.Input.Diff)); err != nil {
		return "", err
	}

	manifest := buildTraceManifest(request, diffFilename)
	if err := writeTraceResult(runDir, request.Result, &manifest); err != nil {
		return "", err
	}

	if err := writeTraceJSON(runDir, "manifest.json", manifest); err != nil {
		return "", err
	}
	complete = true
	return runDir, nil
}

func writeTraceResult(runDir string, result *reviewapp.ReviewResult, manifest *traceManifest) error {
	if result == nil {
		return nil
	}
	manifest.Coverage = result.Coverage
	if err := writeTraceJSON(runDir, "review.json", result.Review); err != nil {
		return err
	}
	manifest.ReviewJSONFile = "review.json"
	if result.Raw != "" {
		if err := writeTraceFile(runDir, "review.xml", []byte(result.Raw)); err != nil {
			return err
		}
		manifest.ReviewXMLFile = "review.xml"
	}
	return writeTraceAngles(runDir, result.Angles, manifest)
}

func writeTraceAngles(runDir string, angles []agentreview.AngleResult, manifest *traceManifest) error {
	for index, angle := range angles {
		rawFilename := fmt.Sprintf("angle-%02d-%s.raw.txt", index+1, safeTraceName(angle.Angle))
		if err := writeTraceFile(runDir, rawFilename, []byte(angle.Raw)); err != nil {
			return err
		}
		manifest.Angles = append(manifest.Angles, traceAngleResult{
			Angle:       angle.Angle,
			Status:      angle.Status,
			Iterations:  angle.Iterations,
			TokensIn:    angle.TokensIn,
			TokensOut:   angle.TokensOut,
			Suggestions: angle.Suggestions,
			RawFile:     rawFilename,
		})
	}
	return nil
}

func buildTraceManifest(request TraceRequest, diffFilename string) traceManifest {
	status := "completed"
	errorMessage := ""
	if request.Err != nil {
		status = "failed"
		errorMessage = request.Err.Error()
	}
	duration := request.FinishedAt.Sub(request.StartedAt)
	if duration < 0 {
		duration = 0
	}
	files := make([]string, 0, len(request.Input.ChangedFiles))
	for _, file := range request.Input.ChangedFiles {
		files = append(files, file.Filename)
	}
	timeout := request.Options.Timeout
	if timeout <= 0 {
		timeout = agentreview.DefaultAngleTimeout
	}
	maxIterations := request.Options.MaxIterations
	if maxIterations <= 0 {
		maxIterations = agentreview.DefaultMaxIterations
	}
	contextWindow := request.Options.ContextWindow
	if contextWindow <= 0 {
		contextWindow = agentreview.DefaultContextWindow
	}
	return traceManifest{
		SchemaVersion:  traceSchemaVersion,
		Status:         status,
		StartedAt:      request.StartedAt,
		FinishedAt:     request.FinishedAt,
		DurationMillis: duration.Milliseconds(),
		Repository:     request.Input.Repository,
		Model:          request.Model,
		Options: traceOptions{
			AngleTimeoutMillis: timeout.Milliseconds(),
			MaxIterations:      maxIterations,
			ContextWindow:      contextWindow,
			Review:             request.Options.Config,
		},
		ChangedFiles:   files,
		CommitMessages: append([]string(nil), request.Input.CommitMessages...),
		DiffFile:       diffFilename,
		Error:          errorMessage,
	}
}

func writeTraceJSON(runDir, name string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode trace %s: %w", name, err)
	}
	data = append(data, '\n')
	return writeTraceFile(runDir, name, data)
}

func writeTraceFile(runDir, name string, data []byte) error {
	if err := os.WriteFile(filepath.Join(runDir, name), data, 0o600); err != nil {
		return fmt.Errorf("write trace %s: %w", name, err)
	}
	return nil
}

func safeTraceName(name string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			builder.WriteRune(r)
			continue
		}
		if builder.Len() > 0 && !strings.HasSuffix(builder.String(), "-") {
			builder.WriteByte('-')
		}
	}
	cleaned := strings.Trim(builder.String(), "-")
	if cleaned == "" {
		return "unknown"
	}
	return cleaned
}
