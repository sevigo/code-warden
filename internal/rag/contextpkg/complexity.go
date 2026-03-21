// Package contextpkg provides context building for code reviews.
package contextpkg

import (
	"fmt"
	"regexp"
	"strings"

	internalgithub "github.com/sevigo/code-warden/internal/github"
)

type FunctionComplexity struct {
	Name       string
	File       string
	StartLine  int
	Complexity int
}

func (b *builderImpl) GatherComplexityContext(changedFiles []internalgithub.ChangedFile) string {
	var allInfos []FunctionComplexity

	for _, cf := range changedFiles {
		if !isCodeFile(cf.Filename) {
			continue
		}
		infos := analyzeFileComplexity(cf.Filename, cf.Patch)
		allInfos = append(allInfos, infos...)
	}

	if len(allInfos) == 0 {
		return ""
	}

	lines := formatHighComplexity(allInfos)
	if len(lines) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("### ⚠️ High Complexity Functions\n")
	sb.WriteString("The following functions have high cyclomatic complexity. ")
	sb.WriteString("Consider refactoring to improve maintainability:\n\n")
	for _, line := range lines {
		sb.WriteString(line)
		sb.WriteString("\n")
	}

	return sb.String()
}

func formatHighComplexity(infos []FunctionComplexity) []string {
	var lines []string
	threshold := 15

	for _, fn := range infos {
		if fn.Complexity > threshold {
			lines = append(lines, fmt.Sprintf("- **%s** (%s:%d): Complexity=%d",
				fn.Name, fn.File, fn.StartLine, fn.Complexity))
		}
	}
	return lines
}

func analyzeFileComplexity(filename, patch string) []FunctionComplexity {
	var results []FunctionComplexity

	lines := strings.Split(patch, "\n")
	controlRegex := regexp.MustCompile(`(?i)^\s*(?:if|else\s*if|for|while|switch|case|catch|when)\b`)
	fnNameRegex := regexp.MustCompile(`^[\t ]*(?:func|def|function|class|public|private|protected)\s+(\w+)`)

	var inFunction bool
	var fnName string
	var fnStart, complexity int

	for _, line := range lines {
		if strings.HasPrefix(line, "@@") {
			fnStart = parseHunkLine(line)
			continue
		}

		if strings.HasPrefix(line, "-") {
			continue
		}

		if !strings.HasPrefix(line, "+") || strings.HasPrefix(line, "+++") {
			continue
		}

		content := strings.TrimPrefix(line, "+")

		if matches := fnNameRegex.FindStringSubmatch(content); matches != nil && !inFunction {
			fnName = matches[1]
			inFunction = true
			complexity = 1
		}

		if !inFunction {
			continue
		}

		complexity += countComplexity(content, controlRegex)

		if strings.Count(content, "}") > strings.Count(content, "{") {
			inFunction = false
			if complexity > 10 {
				results = append(results, FunctionComplexity{
					Name:       fnName,
					File:       filename,
					StartLine:  fnStart,
					Complexity: complexity,
				})
			}
		}
	}

	return results
}

func countComplexity(line string, controlRegex *regexp.Regexp) int {
	count := 0
	if controlRegex.MatchString(line) {
		count++
	}
	count += strings.Count(line, "&&")
	count += strings.Count(line, "||")
	return count
}

func parseHunkLine(hunkLine string) int {
	re := regexp.MustCompile(`@@ -\d+(?:,\d+)? \+(\d+)`)
	matches := re.FindStringSubmatch(hunkLine)
	if len(matches) > 1 {
		var line int
		for _, c := range matches[1] {
			if c >= '0' && c <= '9' {
				line = line*10 + int(c-'0')
			}
		}
		return line
	}
	return 1
}

func isCodeFile(filename string) bool {
	ext := strings.ToLower(filename)
	codeExts := []string{".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".java", ".rs", ".c", ".cpp", ".cs"}
	for _, codeExt := range codeExts {
		if strings.HasSuffix(ext, codeExt) {
			return true
		}
	}
	return false
}
