package gitutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParsePullRequestURL(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		wantOwner string
		wantRepo  string
		wantID    int
		wantErr   bool
	}{
		{
			name:      "Valid HTTPS URL",
			url:       "https://github.com/sevigo/code-warden/pull/123",
			wantOwner: "sevigo",
			wantRepo:  "code-warden",
			wantID:    123,
			wantErr:   false,
		},
		{
			name:      "Valid URL without scheme",
			url:       "github.com/sevigo/code-warden/pull/456",
			wantOwner: "sevigo",
			wantRepo:  "code-warden",
			wantID:    456,
			wantErr:   false,
		},
		{
			name:      "URL with trailing slash",
			url:       "https://github.com/sevigo/code-warden/pull/789/",
			wantOwner: "sevigo",
			wantRepo:  "code-warden",
			wantID:    789,
			wantErr:   false,
		},
		{
			name:    "Invalid PR ID",
			url:     "https://github.com/sevigo/code-warden/pull/abc",
			wantErr: true,
		},
		{
			name:    "Invalid format (missing pull)",
			url:     "https://github.com/sevigo/code-warden/issues/123",
			wantErr: true,
		},
		{
			name:    "Invalid format (too many segments)",
			url:     "https://github.com/sevigo/code-warden/pull/123/files",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo, id, err := ParsePullRequestURL(tt.url)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.wantOwner, owner)
				assert.Equal(t, tt.wantRepo, repo)
				assert.Equal(t, tt.wantID, id)
			}
		})
	}
}
