package render

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sevigo/code-warden/internal/core"
)

func TestRenderVerdictApprove(t *testing.T) {
	SetEnabled(true)
	defer SetEnabled(false)

	var buf bytes.Buffer
	Render(&buf, &core.StructuredReview{
		Verdict: "APPROVE",
		Summary: "Looks good",
		Suggestions: []core.Suggestion{
			{FilePath: "a.go", LineNumber: 5, Severity: "Low", Comment: "nit"},
		},
	}, Options{Width: 80})

	out := buf.String()
	if !strings.Contains(out, "APPROVE") {
		t.Errorf("missing APPROVE verdict, got:\n%s", out)
	}
	if !strings.Contains(out, "a.go:5") {
		t.Errorf("missing file:line, got:\n%s", out)
	}
	if !strings.Contains(out, "nit") {
		t.Errorf("missing comment, got:\n%s", out)
	}
}

func TestRenderNoFindings(t *testing.T) {
	SetEnabled(false)
	var buf bytes.Buffer
	Render(&buf, &core.StructuredReview{
		Verdict:     "APPROVE",
		Summary:     "all good",
		Suggestions: nil,
	}, Options{Width: 80})
	if !strings.Contains(buf.String(), "No findings") {
		t.Errorf("expected 'No findings', got:\n%s", buf.String())
	}
}

func TestRenderConfidence(t *testing.T) {
	SetEnabled(false)
	var buf bytes.Buffer
	Render(&buf, &core.StructuredReview{
		Verdict:    "APPROVE",
		Confidence: 92,
		Summary:    "ok",
	}, Options{Width: 80})
	out := buf.String()
	if !strings.Contains(out, "92") {
		t.Errorf("expected confidence 92, got:\n%s", out)
	}
}

func TestRenderStructuredComment(t *testing.T) {
	SetEnabled(false)
	var buf bytes.Buffer
	Render(&buf, &core.StructuredReview{
		Verdict: "REQUEST_CHANGES",
		Suggestions: []core.Suggestion{
			{
				FilePath:   "main.go",
				LineNumber: 25,
				Severity:   "High",
				Category:   "Bug",
				Source:     "diff:L25",
				Comment: "**Observation:** The function has a bug.\n" +
					"**Rationale:** It panics on negative input.\n" +
					"**Fix:** Add a lower-bound check.\n" +
					"```go\nif n < 0 {\n    n = 0\n}\n```",
			},
		},
	}, Options{Width: 80})

	out := buf.String()
	if !strings.Contains(out, "Observation") {
		t.Errorf("missing Observation label, got:\n%s", out)
	}
	if !strings.Contains(out, "Rationale") {
		t.Errorf("missing Rationale label, got:\n%s", out)
	}
	if !strings.Contains(out, "Fix") {
		t.Errorf("missing Fix label, got:\n%s", out)
	}
	if !strings.Contains(out, "if n < 0") {
		t.Errorf("missing code block, got:\n%s", out)
	}
}

func TestRenderCategoryAndSource(t *testing.T) {
	SetEnabled(false)
	var buf bytes.Buffer
	Render(&buf, &core.StructuredReview{
		Verdict: "REQUEST_CHANGES",
		Suggestions: []core.Suggestion{
			{
				FilePath:   "main.go",
				LineNumber: 10,
				Severity:   "Medium",
				Category:   "Security",
				Source:     "diff:L10",
				Comment:    "test",
			},
		},
	}, Options{Width: 80})

	out := buf.String()
	if !strings.Contains(out, "Security") {
		t.Errorf("missing category, got:\n%s", out)
	}
	if !strings.Contains(out, "diff:L10") {
		t.Errorf("missing source, got:\n%s", out)
	}
}

func TestSeverityRank(t *testing.T) {
	cases := []struct {
		sev  string
		want int
	}{
		{"Critical", 4}, {"HIGH", 3}, {"Medium", 2}, {"low", 1}, {"unknown", 0},
	}
	for _, c := range cases {
		if got := severityRank(c.sev); got != c.want {
			t.Errorf("severityRank(%q) = %d, want %d", c.sev, got, c.want)
		}
	}
}

func TestSortsBySeverity(t *testing.T) {
	SetEnabled(false)
	var buf bytes.Buffer
	Render(&buf, &core.StructuredReview{
		Verdict: "REQUEST_CHANGES",
		Suggestions: []core.Suggestion{
			{FilePath: "b.go", LineNumber: 1, Severity: "Low"},
			{FilePath: "a.go", LineNumber: 1, Severity: "Critical"},
		},
	}, Options{Width: 80})

	out := buf.String()
	crit := strings.Index(out, "a.go:1")
	low := strings.Index(out, "b.go:1")
	if crit == -1 || low == -1 {
		t.Fatalf("expected both findings, got:\n%s", out)
	}
	if crit > low {
		t.Errorf("Critical finding should render before Low; got critical at %d, low at %d", crit, low)
	}
}

func TestParseCommentSections(t *testing.T) {
	comment := "**Observation:** Bug here.\n**Fix:** Do this.\n```go\nx = 1\n```"
	sections := parseCommentSections(comment)
	if len(sections) != 3 {
		t.Fatalf("expected 3 sections, got %d: %+v", len(sections), sections)
	}
	if sections[0].kind != sectionLabel || sections[0].label != "Observation" {
		t.Errorf("section 0: expected label Observation, got %+v", sections[0])
	}
	if sections[1].kind != sectionLabel || sections[1].label != "Fix" {
		t.Errorf("section 1: expected label Fix, got %+v", sections[1])
	}
	if sections[2].kind != sectionCode {
		t.Errorf("section 2: expected code, got %+v", sections[2])
	}
}

func TestExtractLabel(t *testing.T) {
	label, rest := extractLabel("**Observation:** hello")
	if label != "Observation" {
		t.Errorf("expected Observation, got %q", label)
	}
	if rest != "hello" {
		t.Errorf("expected 'hello', got %q", rest)
	}

	label, _ = extractLabel("no label here")
	if label != "" {
		t.Errorf("expected empty label, got %q", label)
	}
}
