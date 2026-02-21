package llm

const (
	extensionGo    = ".go"
	extensionJS    = ".js"
	extensionTS    = ".ts"
	extensionTSX   = ".tsx"
	extensionJSX   = ".jsx"
	extensionPy    = ".py"
	extensionJava  = ".java"
	extensionC     = ".c"
	extensionCpp   = ".cpp"
	extensionH     = ".h"
	extensionHPP   = ".hpp"
	extensionRS    = ".rs"
	extensionRB    = ".rb"
	extensionPHP   = ".php"
	extensionCS    = ".cs"
	extensionSwift = ".swift"
	extensionKT    = ".kt"
	extensionScala = ".scala"
)

// IsCodeExtension returns true if the typical code extension passes
func IsCodeExtension(ext string) bool {
	switch ext {
	case extensionGo, extensionJS, extensionTS, extensionTSX, extensionJSX,
		extensionPy, extensionJava, extensionC, extensionCpp, extensionH,
		extensionHPP, extensionRS, extensionRB, extensionPHP, extensionCS,
		extensionSwift, extensionKT, extensionScala:
		return true
	default:
		return false
	}
}
