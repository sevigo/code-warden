package index

import (
	"path/filepath"
	"regexp"
	"strings"
)

var (
	testFuncNameRegex = regexp.MustCompile(`func\s+\((\w+)\s+\*?(\w+)\)\s*Test(\w+)`)
	testNameRegex     = regexp.MustCompile(`func\s+Test(\w+)`)
	benchmarkRegex    = regexp.MustCompile(`func\s+Benchmark(\w+)`)
	fuzzRegex         = regexp.MustCompile(`func\s+Fuzz(\w+)`)
)

// TestedSymbol represents a mapping from a test to the symbol it tests.
type TestedSymbol struct {
	Symbol     string // The production symbol being tested (e.g., "UserService", "HandleRequest")
	SymbolType string // Type of symbol: "function", "method", "type"
	SourceFile string // Inferred source file (e.g., "service.go" for "service_test.go")
}

// ExtractTestedSymbols extracts the symbols being tested from a test file's content.
// This uses naming conventions and patterns common in Go test files:
//   - TestFoo tests Foo (function or type)
//   - TestFoo_Bar tests Foo.Bar (method)
//   - BenchmarkFoo tests Foo (performance characteristics)
//   - FuzzFoo tests Foo (fuzz targets)
func ExtractTestedSymbols(filePath string, content string) []TestedSymbol {
	if !IsTestFile(filePath) {
		return nil
	}

	symbols := make(map[string]TestedSymbol)
	ext := strings.ToLower(filepath.Ext(filePath))

	switch ext {
	case extGo:
		extractGoTestedSymbols(content, symbols)
	case extTypeScript, extTSX:
		extractTSTestedSymbols(content, symbols)
	case extPython:
		extractPythonTestedSymbols(content, symbols)
	case ".java":
		extractJavaTestedSymbols(content, symbols)
	case ".rs":
		extractRustTestedSymbols(content, symbols)
	}

	var result []TestedSymbol
	for _, s := range symbols {
		result = append(result, s)
	}
	return result
}

func extractGoTestedSymbols(content string, symbols map[string]TestedSymbol) {
	lines := strings.Split(content, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "func ") {
			continue
		}

		// Try method test pattern first: func (recv *Type) TestXxx
		if matches := testFuncNameRegex.FindStringSubmatch(line); len(matches) == 4 {
			extractMethodTestSymbol(matches, symbols)
			continue
		}

		// Try regular test pattern: func TestXxx
		if matches := testNameRegex.FindStringSubmatch(line); len(matches) == 2 {
			extractFunctionTestSymbol(matches[1], symbols)
			continue
		}

		// Try benchmark pattern: func BenchmarkXxx
		if matches := benchmarkRegex.FindStringSubmatch(line); len(matches) == 2 {
			symbols[matches[1]] = TestedSymbol{Symbol: matches[1], SymbolType: "function"}
			continue
		}

		// Try fuzz pattern: func FuzzXxx
		if matches := fuzzRegex.FindStringSubmatch(line); len(matches) == 2 {
			symbols[matches[1]] = TestedSymbol{Symbol: matches[1], SymbolType: "function"}
		}
	}
}

func extractMethodTestSymbol(matches []string, symbols map[string]TestedSymbol) {
	receiver := matches[2]
	testName := matches[3]

	if strings.Contains(testName, "_") {
		parts := strings.SplitN(testName, "_", 2)
		if len(parts) == 2 {
			receiver = parts[0]
			testName = parts[1]
		}
	}

	// Only add if symbol name differs from receiver
	if !strings.HasPrefix(testName, receiver) {
		symbols[receiver+"."+testName] = TestedSymbol{
			Symbol:     testName,
			SymbolType: "method",
		}
	}
}

func extractFunctionTestSymbol(testName string, symbols map[string]TestedSymbol) {
	if strings.Contains(testName, "_") {
		parts := strings.SplitN(testName, "_", 2)
		if len(parts) == 2 {
			symbols[parts[0]] = TestedSymbol{
				Symbol:     parts[0],
				SymbolType: "function",
			}
		}
		return
	}
	symbols[testName] = TestedSymbol{
		Symbol:     testName,
		SymbolType: "function",
	}
}

func extractTSTestedSymbols(content string, symbols map[string]TestedSymbol) {
	describeRegex := regexp.MustCompile(`(?:describe|it|test)\s*\(\s*['"]([^'"]+)['"]`)
	if matches := describeRegex.FindAllStringSubmatch(content, -1); len(matches) > 0 {
		for _, m := range matches {
			testName := m[1]
			if strings.Contains(testName, "should") || strings.Contains(testName, "when") {
				continue
			}
			parts := strings.Fields(testName)
			if len(parts) > 0 {
				symbol := strings.TrimSuffix(parts[0], "s")
				symbols[symbol] = TestedSymbol{
					Symbol:     symbol,
					SymbolType: "function",
				}
			}
		}
	}
}

func extractPythonTestedSymbols(content string, symbols map[string]TestedSymbol) {
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "def test_") {
			funcName := strings.TrimPrefix(line, "def test_")
			if idx := strings.Index(funcName, "("); idx > 0 {
				funcName = funcName[:idx]
			}
			symbols[funcName] = TestedSymbol{
				Symbol:     funcName,
				SymbolType: "function",
			}
		}
	}
}

func extractJavaTestedSymbols(content string, symbols map[string]TestedSymbol) {
	testMethodRegex := regexp.MustCompile(`@Test\s+(?:public\s+)?void\s+(?:test)?(\w+)\s*\(`)
	if matches := testMethodRegex.FindAllStringSubmatch(content, -1); len(matches) > 0 {
		for _, m := range matches {
			symbols[m[1]] = TestedSymbol{
				Symbol:     m[1],
				SymbolType: "function",
			}
		}
	}
}

func extractRustTestedSymbols(content string, symbols map[string]TestedSymbol) {
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#[test]") ||
			strings.Contains(line, "fn test_") ||
			strings.HasPrefix(line, "fn test_") {
			if strings.Contains(line, "fn ") {
				parts := strings.Split(line, "fn ")
				if len(parts) > 1 {
					funcName := strings.Split(parts[1], "(")[0]
					funcName = strings.TrimPrefix(funcName, "test_")
					symbols[funcName] = TestedSymbol{
						Symbol:     funcName,
						SymbolType: "function",
					}
				}
			}
		}
	}
}

// InferSourceFile infers the production source file from a test file path.
// Example: "service_test.go" -> "service.go", "handler.test.ts" -> "handler.ts"
func InferSourceFile(testFile string) string {
	dir := filepath.Dir(testFile)
	base := filepath.Base(testFile)
	ext := filepath.Ext(base)

	switch ext {
	case ".go":
		if strings.HasSuffix(base, "_test.go") {
			sourceName := strings.TrimSuffix(base, "_test.go") + ".go"
			return filepath.Join(dir, sourceName)
		}
		return testFile
	case ".ts", ".tsx", ".js", ".jsx":
		sourceName := strings.TrimSuffix(base, ext)
		sourceName = strings.TrimSuffix(sourceName, ".test")
		sourceName = strings.TrimSuffix(sourceName, ".spec")
		return filepath.Join(dir, sourceName+ext)
	case ".py":
		if strings.HasSuffix(base, "_test.py") {
			sourceName := strings.TrimSuffix(base, "_test.py") + ".py"
			return filepath.Join(dir, sourceName)
		}
		if strings.HasSuffix(base, "test_") {
			if len(base) > 5 {
				sourceName := base[5:]
				return filepath.Join(dir, sourceName)
			}
		}
		return testFile
	default:
		return testFile
	}
}
