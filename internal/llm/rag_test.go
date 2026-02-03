package llm

import (
	"reflect"
	"sort"
	"testing"
)

func TestExtractSymbolsFromPatch(t *testing.T) {
	r := &ragService{}

	tests := []struct {
		name     string
		patch    string
		expected []string
	}{
		{
			name: "Simple function",
			patch: `+func HelloWorld() {
+	fmt.Println("Hello")
+}`,
			expected: []string{"HelloWorld"},
		},
		{
			name: "Function with receiver",
			patch: `+func (r *ragService) extractSymbolsFromPatch(patch string) []string {
+	return nil
+}`,
			expected: []string{"extractSymbolsFromPatch"},
		},
		{
			name: "Type definition",
			patch: `+type MyStruct struct {
+	Field string
+}`,
			expected: []string{"MyStruct"},
		},
		{
			name: "Interface definition",
			patch: `+type MyInterface interface {
+	DoSomething()
+}`,
			expected: []string{"MyInterface"},
		},
		{
			name: "Multiple symbols",
			patch: `+func Alpha() {}
-func Beta() {}
+type Gamma struct{}
+func (s *Gamma) Delta() {}`,
			expected: []string{"Alpha", "Gamma", "Delta"},
		},
		{
			name: "No added symbols",
			patch: `-func OldFunc() {}
 	fmt.Println("No change")`,
			expected: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := r.extractSymbolsFromPatch(tt.patch)
			sort.Strings(actual)
			sort.Strings(tt.expected)
			if !reflect.DeepEqual(actual, tt.expected) {
				t.Errorf("expected %v, got %v", tt.expected, actual)
			}
		})
	}
}

func TestMatchFuncSymbol(t *testing.T) {
	r := &ragService{}

	tests := []struct {
		line     string
		expected string
	}{
		{"func HelloWorld() {", "HelloWorld"},
		{"func (r *Receiver) Method(arg string) error {", "Method"},
		{"func (r Receiver) Method() {", "Method"},
		{"func Generic[T any](t T) {", "Generic"},
		{"not a func", ""},
		{"func ", ""},
	}

	for _, tt := range tests {
		actual := r.matchFuncSymbol(tt.line)
		if actual != tt.expected {
			t.Errorf("line: %s, expected: %s, got: %s", tt.line, tt.expected, actual)
		}
	}
}

func TestMatchTypeSymbol(t *testing.T) {
	r := &ragService{}

	tests := []struct {
		line     string
		expected string
	}{
		{"type MyStruct struct {", "MyStruct"},
		{"type MyInterface interface {", "MyInterface"},
		{"type MyInt int", "MyInt"},
		{"not a type", ""},
		{"type ", ""},
	}

	for _, tt := range tests {
		actual := r.matchTypeSymbol(tt.line)
		if actual != tt.expected {
			t.Errorf("line: %s, expected: %s, got: %s", tt.line, tt.expected, actual)
		}
	}
}
