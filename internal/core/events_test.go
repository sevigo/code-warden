package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCommand(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		cmd     string
		instr   string
		wantErr bool
	}{
		{name: "review", body: "/review", cmd: "review", instr: ""},
		{name: "rereview", body: "/rereview", cmd: "review", instr: ""},
		{name: "readiness", body: "/readiness", cmd: "readiness", instr: ""},
		{name: "review with instructions", body: "/review check security", cmd: "review", instr: "check security"},
		{name: "readiness with instructions", body: "/readiness focus payments", cmd: "readiness", instr: "focus payments"},
		{name: "empty", body: "", wantErr: true},
		{name: "not a command", body: "hello", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd, instr, err := parseCommand(tc.body)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.cmd, cmd)
			assert.Equal(t, tc.instr, instr)
		})
	}
}

func TestParseCommandSanitizesInstructions(t *testing.T) {
	_, instr, err := parseCommand("/review check\nsecurity")
	require.NoError(t, err)
	assert.Equal(t, "check security", instr)
}
