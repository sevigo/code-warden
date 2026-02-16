package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// CleanOldRepos removes repository directories that haven't been accessed 
// in the specified duration.
func CleanOldRepos(basePath string, dryRun bool, maxAge time.Duration) error {
	entries, err := os.ReadDir(basePath)
	if err != nil {
		return fmt.Errorf("failed to read directory: %w", err)
	}

	for _, entry := range entries {
		fullPath := filepath.Join(basePath, entry.Name())

		info, err := os.Stat(fullPath)
		if err != nil {
			continue
		}

		if time.Since(info.ModTime()) > maxAge {
			if dryRun {
				fmt.Printf("Would delete: %s\n", fullPath)
				continue
			}
			
			err := os.RemoveAll(fullPath)
			if err != nil {
				return err
			}
		}
	}
	return nil
}