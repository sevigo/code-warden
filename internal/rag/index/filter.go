package index

import (
	"path/filepath"
	"strings"

	"github.com/sevigo/code-warden/internal/core"
)

// FilterFilesByExtensions removes files whose extension matches an excluded extension.
func FilterFilesByExtensions(files []string, excludeExts []string) []string {
	if len(excludeExts) == 0 {
		return files
	}

	excludeMap := make(map[string]struct{}, len(excludeExts))
	for _, ext := range excludeExts {
		normalizedExt := strings.ToLower(strings.TrimPrefix(ext, "."))
		excludeMap[normalizedExt] = struct{}{}
	}

	filtered := make([]string, 0, len(files))
	for _, file := range files {
		fileExt := strings.ToLower(strings.TrimPrefix(filepath.Ext(file), "."))
		if _, isExcluded := excludeMap[fileExt]; !isExcluded {
			filtered = append(filtered, file)
		}
	}

	return filtered
}

// FilterFilesByValidExtensions keeps only files with whitelisted extensions.
func FilterFilesByValidExtensions(files []string) []string {
	filtered := make([]string, 0, len(files))
	for _, file := range files {
		ext := strings.ToLower(filepath.Ext(file))
		if core.IsValidExtension(ext) {
			filtered = append(filtered, file)
		}
	}
	return filtered
}

// BuildExcludeDirs combines default and user-configured directory exclusions.
func BuildExcludeDirs(repoConfig *core.RepoConfig) []string {
	return core.BuildExcludeDirs(repoConfig.ExcludeDirs)
}

// FilterFilesByDirectories removes files located within any excluded directory.
func FilterFilesByDirectories(files []string, excludeDirs []string) []string {
	if len(excludeDirs) == 0 {
		return files
	}

	filtered := make([]string, 0, len(files))
	for _, file := range files {
		cleanFile := filepath.ToSlash(filepath.Clean(strings.TrimPrefix(file, string(filepath.Separator))))

		isExcluded := false
		for _, excludeDir := range excludeDirs {
			cleanExcludeDir := filepath.Clean(excludeDir)

			// Check if the file path is exactly the excluded directory
			if cleanFile == cleanExcludeDir {
				isExcluded = true
				break
			}

			// Check if the file path starts with the excluded directory followed by a separator
			// Use forward slash for cross-platform consistency
			if strings.HasPrefix(cleanFile, cleanExcludeDir+"/") {
				isExcluded = true
				break
			}
		}

		if !isExcluded {
			filtered = append(filtered, file)
		}
	}

	return filtered
}

// FilterFilesBySpecificFiles removes files matching any excluded file path.
func FilterFilesBySpecificFiles(files []string, excludeFiles []string) []string {
	if len(excludeFiles) == 0 {
		return files
	}

	excludeMap := make(map[string]struct{}, len(excludeFiles))
	for _, f := range excludeFiles {
		excludeMap[filepath.ToSlash(filepath.Clean(f))] = struct{}{}
	}

	filtered := make([]string, 0, len(files))
	for _, file := range files {
		if _, isExcluded := excludeMap[filepath.ToSlash(filepath.Clean(file))]; !isExcluded {
			filtered = append(filtered, file)
		}
	}

	return filtered
}
