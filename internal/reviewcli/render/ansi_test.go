package render

import (
	"strings"
	"testing"
)

func TestColorHelpersDisabled(t *testing.T) {
	SetEnabled(false)
	defer SetEnabled(true)

	if got := Red("hi"); got != "hi" {
		t.Errorf("Red with colors disabled = %q, want %q", got, "hi")
	}
	if got := Bold("x"); got != "x" {
		t.Errorf("Bold with colors disabled = %q, want %q", got, "x")
	}
}

func TestColorHelpersEnabled(t *testing.T) {
	SetEnabled(true)
	defer SetEnabled(false)

	if got := Red("hi"); got != "\x1b[31mhi\x1b[0m" {
		t.Errorf("Red with colors enabled = %q", got)
	}
	if got := GreenBold("ok"); !strings.Contains(got, "\x1b[1;32m") {
		t.Errorf("GreenBold = %q, missing bold green code", got)
	}
}

func TestWrap(t *testing.T) {
	SetEnabled(false)
	in := "the quick brown fox jumps over the lazy dog"
	got := Wrap(in, 20)
	lines := strings.Split(got, "\n")
	for _, l := range lines {
		if len(l) > 20 {
			t.Errorf("line exceeds width: %q (%d)", l, len(l))
		}
	}
	if got == "" {
		t.Error("Wrap returned empty")
	}
}

func TestWrapPreservesParagraphBreaks(t *testing.T) {
	in := "para one\n\npara two"
	got := Wrap(in, 80)
	if !strings.Contains(got, "para one") || !strings.Contains(got, "para two") {
		t.Errorf("Wrap lost content: %q", got)
	}
}
