package index

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFiltering(t *testing.T) {
	t.Run("FilterFilesByExtensions", func(t *testing.T) {
		tests := []struct {
			name    string
			files   []string
			exclude []string
			want    []string
		}{
			{
				name:    "basic filtering",
				files:   []string{"main.go", "config.yml", "go.sum", "README.md"},
				exclude: []string{".yml", "sum"},
				want:    []string{"main.go", "README.md"},
			},
			{
				name:    "no exclusions",
				files:   []string{"main.go", "utils.go"},
				exclude: []string{},
				want:    []string{"main.go", "utils.go"},
			},
			{
				name:    "exclude all",
				files:   []string{"config.yml", "go.sum"},
				exclude: []string{".yml", ".sum"},
				want:    []string{},
			},
			{
				name:    "extension without dot",
				files:   []string{"main.go", "config.yml"},
				exclude: []string{"yml"},
				want:    []string{"main.go"},
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := FilterFilesByExtensions(tt.files, tt.exclude)
				assert.Equal(t, tt.want, got)
			})
		}
	})

	t.Run("FilterFilesByDirectories", func(t *testing.T) {
		tests := []struct {
			name    string
			files   []string
			exclude []string
			want    []string
		}{
			{
				name: "basic directory filtering",
				files: []string{
					"src/main.go",
					"vendor/pkg/a.go",
					"node_modules/lib/b.js",
					"internal/pkg/c.go",
					".vscode/settings.json",
				},
				exclude: []string{"vendor", "node_modules", ".vscode"},
				want:    []string{"src/main.go", "internal/pkg/c.go"},
			},
			{
				name:    "no exclusions",
				files:   []string{"src/main.go", "pkg/util.go"},
				exclude: []string{},
				want:    []string{"src/main.go", "pkg/util.go"},
			},
			{
				name:    "exclude all",
				files:   []string{"vendor/a.go", "node_modules/b.js"},
				exclude: []string{"vendor", "node_modules"},
				want:    []string{},
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := FilterFilesByDirectories(tt.files, tt.exclude)
				assert.Equal(t, tt.want, got)
			})
		}
	})

	t.Run("FilterFilesBySpecificFiles", func(t *testing.T) {
		tests := []struct {
			name    string
			files   []string
			exclude []string
			want    []string
		}{
			{
				name:    "basic file filtering",
				files:   []string{"main.go", "config/secrets.json", "scripts/temp.py", "README.md"},
				exclude: []string{"config/secrets.json", "scripts/temp.py"},
				want:    []string{"main.go", "README.md"},
			},
			{
				name:    "no exclusions",
				files:   []string{"main.go", "utils.go"},
				exclude: []string{},
				want:    []string{"main.go", "utils.go"},
			},
			{
				name:    "exclude all",
				files:   []string{"config.yaml", "Dockerfile"},
				exclude: []string{"config.yaml", "Dockerfile"},
				want:    []string{},
			},
			{
				name:    "path normalization with dot",
				files:   []string{"main.go", "config/secrets.json"},
				exclude: []string{"./config/secrets.json"},
				want:    []string{"main.go"},
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := FilterFilesBySpecificFiles(tt.files, tt.exclude)
				assert.Equal(t, tt.want, got)
			})
		}
	})

	t.Run("FilterFilesByValidExtensions", func(t *testing.T) {
		tests := []struct {
			name  string
			files []string
			want  []string
		}{
			{
				name:  "mixed valid and invalid",
				files: []string{"main.go", "config.exe", "README.md", "binary.dll"},
				want:  []string{"main.go", "README.md"},
			},
			{
				name:  "all valid",
				files: []string{"main.go", "utils.ts", "README.md"},
				want:  []string{"main.go", "utils.ts", "README.md"},
			},
			{
				name:  "all invalid",
				files: []string{"binary.exe", "library.dll", "temp.tmp"},
				want:  []string{},
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := FilterFilesByValidExtensions(tt.files)
				assert.Equal(t, tt.want, got)
			})
		}
	})
}
