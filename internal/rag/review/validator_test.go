package review

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sevigo/code-warden/internal/core"
	internalgithub "github.com/sevigo/code-warden/internal/github"
)

func TestSuggestionValidator_ValidateLineNumber(t *testing.T) {
	files := []internalgithub.ChangedFile{
		{
			Filename: "foo.go",
			Patch: `@@ -1,3 +1,4 @@
 package foo
+var a = 1
+var b = 2
 `,
		},
	}

	v := NewSuggestionValidator("", files)

	valid := &core.Suggestion{FilePath: "foo.go", LineNumber: 2}
	assert.True(t, v.ValidateLineNumber(valid), "added line 2 should be valid")

	invalid := &core.Suggestion{FilePath: "foo.go", LineNumber: 10}
	assert.False(t, v.ValidateLineNumber(invalid), "line 10 not in diff should be invalid")

	missingFile := &core.Suggestion{FilePath: "bar.go", LineNumber: 1}
	assert.False(t, v.ValidateLineNumber(missingFile), "file not in diff should be invalid")
}

func TestSuggestionValidator_ValidateLineNumber_NoFileOrLine(t *testing.T) {
	v := NewSuggestionValidator("", nil)

	assert.True(t, v.ValidateLineNumber(&core.Suggestion{FilePath: "", LineNumber: 0}), "empty file and line should be accepted")
}
