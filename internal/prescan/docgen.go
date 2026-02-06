package prescan

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DocGenerator generates project documentation.
type DocGenerator struct {
	baseDir string
}

func NewDocGenerator(baseDir string) *DocGenerator {
	return &DocGenerator{baseDir: baseDir}
}

// GenerateProjectStructure creates a tree-like structure of the project.
func (d *DocGenerator) GenerateProjectStructure(root string) (string, error) {
	var builder strings.Builder
	builder.WriteString("# Project Structure\n\n")

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if rel == "." {
			return nil
		}

		// Skip hidden
		if strings.HasPrefix(info.Name(), ".") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		depth := strings.Count(rel, string(os.PathSeparator))
		indent := strings.Repeat("  ", depth)

		if info.IsDir() {
			builder.WriteString(fmt.Sprintf("%s- **%s/**\n", indent, info.Name()))
		} else {
			builder.WriteString(fmt.Sprintf("%s- %s\n", indent, info.Name()))
		}
		return nil
	})

	return builder.String(), err
}

// TODO: Implement GenerateDependencyGraph logic requiring parser outputs
