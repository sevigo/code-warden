// Package render provides terminal formatting for review output: a small ANSI
// color helper with auto-detection and structured renderers for the structured
// review result.
package render

import (
	"os"
	"strings"

	"golang.org/x/term"
)

// Enabled controls whether ANSI escape codes are emitted. It is auto-detected
// from the environment: colors are on only when stdout is a terminal and the
// NO_COLOR convention is not set. Callers can force a value via SetEnabled.
var Enabled = detectColor()

// detectColor reports whether colorized output should be used. It checks the
// NO_COLOR environment variable (https://no-color.org) and whether stdout is a
// terminal.
func detectColor() bool {
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	return isTerminal(os.Stdout)
}

// isTerminal reports whether f is a character device (i.e. an interactive
// terminal) rather than a pipe or file.
func isTerminal(f *os.File) bool {
	//nolint:gosec // G115: fd is a small OS file descriptor; uintptr->int is safe here.
	return term.IsTerminal(int(f.Fd()))
}

// SetEnabled overrides the auto-detected color setting.
func SetEnabled(on bool) {
	Enabled = on
}

// ANSI escape sequences. Emitted only when Enabled is true; Reset is always
// safe (a bare reset with no preceding code is a no-op visually).
const (
	reset        = "\x1b[0m"
	bold         = "\x1b[1m"
	dim          = "\x1b[2m"
	red          = "\x1b[31m"
	green        = "\x1b[32m"
	yellow       = "\x1b[33m"
	blue         = "\x1b[34m"
	magenta      = "\x1b[35m"
	cyan         = "\x1b[36m"
	bgRed        = "\x1b[41m"
	bgGreen      = "\x1b[42m"
	bgYellow     = "\x1b[43m"
	bgBlue       = "\x1b[44m"
	bgMagenta    = "\x1b[45m"
	bgCyan       = "\x1b[46m"
	black        = "\x1b[30m"
	fgRedBold    = "\x1b[1;31m"
	fgGreenBold  = "\x1b[1;32m"
	fgYellowBold = "\x1b[1;33m"
	fgBlueBold   = "\x1b[1;34m"
	fgMagentaB   = "\x1b[1;35m"
	fgCyanB      = "\x1b[1;36m"
)

func wrap(code string) func(s string) string {
	return func(s string) string {
		if !Enabled || s == "" {
			return s
		}
		return code + s + reset
	}
}

// Colorized text helpers. Each returns s unchanged when colors are disabled.
var (
	Bold       = wrap(bold)
	Dim        = wrap(dim)
	Red        = wrap(red)
	Green      = wrap(green)
	Yellow     = wrap(yellow)
	Blue       = wrap(blue)
	Magenta    = wrap(magenta)
	Cyan       = wrap(cyan)
	RedBold    = wrap(fgRedBold)
	GreenBold  = wrap(fgGreenBold)
	YellowBold = wrap(fgYellowBold)
	BlueBold   = wrap(fgBlueBold)

	// Background pills for severity labels.
	RedBg    = wrap(bgRed)
	GreenBg  = wrap(bgGreen)
	YellowBg = wrap(bgYellow)
	BlueBg   = wrap(bgBlue)
)

// Wrap reflows text to the given width, preserving existing single newlines
// as paragraph breaks. It does not split words.
func Wrap(s string, width int) string {
	if width <= 0 {
		return s
	}
	var b strings.Builder
	for _, para := range strings.Split(s, "\n") {
		words := strings.Fields(para)
		line := ""
		for _, w := range words {
			switch {
			case line == "":
				line = w
			case len(line)+1+len(w) <= width:
				line += " " + w
			default:
				b.WriteString(line)
				b.WriteString("\n")
				line = w
			}
		}
		if line != "" {
			b.WriteString(line)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
