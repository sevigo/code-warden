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
