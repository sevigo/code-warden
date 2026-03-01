package review

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/storage"
)

// TestValidateConsensusParams tests parameter validation
func TestValidateConsensusParams(t *testing.T) {
	service := &Service{
		cfg: Config{
			Logger: slog.Default(),
		},
	}

	tests := []struct {
		name    string
		repo    *storage.Repository
		event   *core.GitHubEvent
		models  []string
		wantErr string
	}{
		{
			name:    "nil repo",
			repo:    nil,
			event:   &core.GitHubEvent{},
			models:  []string{"model1"},
			wantErr: "repo cannot be nil",
		},
		{
			name:    "nil event",
			repo:    &storage.Repository{},
			event:   nil,
			models:  []string{"model1"},
			wantErr: "event cannot be nil",
		},
		{
			name:    "empty models",
			repo:    &storage.Repository{},
			event:   &core.GitHubEvent{},
			models:  []string{},
			wantErr: "consensus review requires at least one model",
		},
		{
			name:    "valid params",
			repo:    &storage.Repository{},
			event:   &core.GitHubEvent{},
			models:  []string{"model1"},
			wantErr: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := service.validateConsensusParams(tt.repo, tt.event, tt.models)
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
