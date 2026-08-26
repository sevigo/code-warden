package skills

import (
	"fmt"
	"strings"
)

// UnknownCommandError indicates a comment did not name a recognized skill
// command. Callers use it to ignore non-command comments.
type UnknownCommandError struct {
	Command string
}

func (e *UnknownCommandError) Error() string {
	return fmt.Sprintf("comment is not a valid skill command: %q", e.Command)
}

// ParseCommand extracts the command and trailing instructions from a normalized
// comment body. It recognizes "/review" and "/rereview", which run all applicable
// skills and return no skill override. Any other command is returned as an error
// so non-command comments are ignored. Named skill overrides (e.g. "/infra")
// will be added alongside the first analyzer skills.
func ParseCommand(commentBody string) (overrides []string, instructions string, err error) {
	body := strings.TrimSpace(commentBody)
	if body == "" {
		return nil, "", &UnknownCommandError{Command: ""}
	}

	parts := strings.Fields(body)
	cmd := strings.TrimPrefix(parts[0], "/")
	rest := strings.TrimSpace(strings.TrimPrefix(body, parts[0]))

	switch cmd {
	case "review", "rereview":
		return nil, rest, nil
	default:
		return nil, "", &UnknownCommandError{Command: cmd}
	}
}
