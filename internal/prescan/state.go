package prescan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sevigo/code-warden/internal/storage"
)

type ScanStatus string

const (
	StatusPending    ScanStatus = "pending"
	StatusInProgress ScanStatus = "in_progress"
	StatusCompleted  ScanStatus = "completed"
	StatusFailed     ScanStatus = "failed"
)

type Progress struct {
	TotalFiles     int             `json:"total_files"`
	ProcessedFiles int             `json:"processed_files"`
	Files          map[string]bool `json:"files"` // map[filepath]processed
	LastUpdated    time.Time       `json:"last_updated"`
}

type StateManager struct {
	store  storage.Store
	repoID int64
}

func NewStateManager(store storage.Store, repoID int64) *StateManager {
	return &StateManager{
		store:  store,
		repoID: repoID,
	}
}

func (sm *StateManager) LoadState(ctx context.Context) (*storage.ScanState, *Progress, error) {
	state, err := sm.store.GetScanState(ctx, sm.repoID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("failed to load scan state: %w", err)
	}
	if state == nil {
		return nil, nil, nil
	}

	var progress Progress
	if len(state.Progress) > 0 {
		if err := json.Unmarshal(state.Progress, &progress); err != nil {
			return state, nil, fmt.Errorf("failed to unmarshal progress: %w", err)
		}
	}
	return state, &progress, nil
}

func (sm *StateManager) SaveState(ctx context.Context, status ScanStatus, progress *Progress, artifacts map[string]interface{}) error {
	progressJSON, err := json.Marshal(progress)
	if err != nil {
		return fmt.Errorf("failed to marshal progress: %w", err)
	}

	var artifactsJSON json.RawMessage
	if artifacts != nil {
		b, err := json.Marshal(artifacts)
		if err != nil {
			return fmt.Errorf("failed to marshal artifacts: %w", err)
		}
		artifactsJSON = b
	}

	state := &storage.ScanState{
		RepositoryID: sm.repoID,
		Status:       string(status),
		Progress:     progressJSON,
		Artifacts:    &artifactsJSON,
	}

	return sm.store.UpsertScanState(ctx, state)
}
