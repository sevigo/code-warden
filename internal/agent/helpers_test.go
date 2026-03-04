package agent

import (
	"testing"
)

func TestSanitizeSessionID(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		want    string
		wantErr bool
	}{
		{
			name:    "valid simple ID",
			id:      "neo-abc123",
			want:    "neo-abc123",
			wantErr: false,
		},
		{
			name:    "valid with underscores",
			id:      "smith-1a2b",
			want:    "smith-1a2b",
			wantErr: false,
		},
		{
			name:    "valid uppercase",
			id:      "Smith-ABCD",
			want:    "Smith-ABCD",
			wantErr: false,
		},
		{
			name:    "path traversal attempt with ..",
			id:      "../../../etc/passwd",
			want:    "",
			wantErr: true,
		},
		{
			name:    "path traversal in middle",
			id:      "foo/../bar",
			want:    "",
			wantErr: true,
		},
		{
			name:    "hidden file attempt",
			id:      ".hidden",
			want:    "",
			wantErr: true,
		},
		{
			name:    "starts with dot",
			id:      ".config",
			want:    "",
			wantErr: true,
		},
		{
			name:    "special characters",
			id:      "foo@bar",
			want:    "",
			wantErr: true,
		},
		{
			name:    "spaces",
			id:      "foo bar",
			want:    "",
			wantErr: true,
		},
		{
			name:    "path separator",
			id:      "foo/bar",
			want:    "",
			wantErr: true,
		},
		{
			name:    "backslashes",
			id:      "foo\\bar",
			want:    "",
			wantErr: true,
		},
		{
			name:    "empty ID",
			id:      "",
			want:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := sanitizeSessionID(tt.id)
			if (err != nil) != tt.wantErr {
				t.Errorf("sanitizeSessionID() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("sanitizeSessionID() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsSafeChar(t *testing.T) {
	tests := []struct {
		c    rune
		want bool
	}{
		{'a', true},
		{'z', true},
		{'A', true},
		{'Z', true},
		{'0', true},
		{'9', true},
		{'-', true},
		{'_', true},
		{'/', false},
		{'\\', false},
		{'.', false},
		{'@', false},
		{' ', false},
		{'.', false},
	}

	for _, tt := range tests {
		t.Run(string(tt.c), func(t *testing.T) {
			if got := isSafeChar(tt.c); got != tt.want {
				t.Errorf("isSafeChar(%q) = %v, want %v", tt.c, got, tt.want)
			}
		})
	}
}
