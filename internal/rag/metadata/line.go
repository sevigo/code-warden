package metadata

// ExtractLineNumber extracts a line number from document metadata.
// It checks both "line" and "start_line" keys, handling type assertions
// for int, int64, and float64 values (float64 is common from JSON unmarshaling).
func ExtractLineNumber(metadata map[string]any) int {
	if v, ok := metadata["line"]; ok {
		switch val := v.(type) {
		case int:
			return val
		case int64:
			return int(val)
		case float64:
			return int(val)
		}
	}
	if v, ok := metadata["start_line"]; ok {
		switch val := v.(type) {
		case int:
			return val
		case int64:
			return int(val)
		case float64:
			return int(val)
		}
	}
	return 0
}
