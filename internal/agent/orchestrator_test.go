package agent

import (
	"encoding/json"
	"io"
	"log/slog"
	"reflect"
	"testing"
)

func TestParseAgentOutput(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	o := &Orchestrator{logger: logger}

	tests := []struct {
		name     string
		output   string
		expected *Result
	}{
		{
			name:   "clean output with envelope",
			output: "AGENT_RESULT: {\"pr_number\": 123, \"pr_url\": \"https://github.com/org/repo/pull/123\", \"branch\": \"agent/feat\", \"files_changed\": [\"main.go\"], \"verdict\": \"APPROVED\", \"iterations\": 2}",
			expected: &Result{
				PRNumber:     123,
				PRURL:        "https://github.com/org/repo/pull/123",
				Branch:       "agent/feat",
				FilesChanged: []string{"main.go"},
				Verdict:      "APPROVED",
				Iterations:   2,
			},
		},
		{
			name: "noisy output with embedded envelope",
			output: `
Some random logs here
Iteration 1: starting
Running tests...
AGENT_RESULT: {"pr_number": 456, "pr_url": "https://github.com/org/repo/pull/456", "branch": "agent/fix", "files_changed": ["utils.go"], "verdict": "APPROVED", "iterations": 1}
Cleanup...
`,
			expected: &Result{
				PRNumber:     456,
				PRURL:        "https://github.com/org/repo/pull/456",
				Branch:       "agent/fix",
				FilesChanged: []string{"utils.go"},
				Verdict:      "APPROVED",
				Iterations:   1,
			},
		},
		{
			name: "no envelope - fallback branch and iterations",
			output: `
AGENT_ITERATION: 1
AGENT_ITERATION: 2
Done.
`,
			expected: &Result{
				PRNumber:     0,
				PRURL:        "",
				Branch:       "agent/default",
				FilesChanged: []string{},
				Verdict:      "UNKNOWN",
				Iterations:   2,
			},
		},
		{
			name: "malformed JSON in sentinel - fallback",
			output: `
AGENT_ITERATION: 1
AGENT_RESULT: {invalid json}
AGENT_ITERATION: 2
`,
			expected: &Result{
				PRNumber:     0,
				PRURL:        "",
				Branch:       "agent/default",
				FilesChanged: []string{},
				Verdict:      "UNKNOWN",
				Iterations:   2,
			},
		},
		{
			name: "multiple AGENT_RESULT lines - uses first valid",
			output: `
AGENT_ITERATION: 1
AGENT_RESULT: {"pr_number": 1, "verdict": "FAILED"}
AGENT_ITERATION: 2
AGENT_RESULT: {"pr_number": 2, "verdict": "APPROVED"}
`,
			expected: &Result{
				PRNumber:   1,
				Verdict:    "FAILED",
				Iterations: 0, // Result parsing doesn't count iterations
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := o.parseAgentOutput(tt.output, "agent/default")
			if !reflect.DeepEqual(got, tt.expected) {
				gotJSON, _ := json.Marshal(got)
				expJSON, _ := json.Marshal(tt.expected)
				t.Errorf("parseAgentOutput() = %s, want %s", string(gotJSON), string(expJSON))
			}
		})
	}
}
