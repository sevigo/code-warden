package rag

import (
	"reflect"
	"testing"

	internalgithub "github.com/sevigo/code-warden/internal/github"
)

func TestParseDiff(t *testing.T) {
	tests := []struct {
		name string
		diff string
		want []internalgithub.ChangedFile
	}{
		{
			name: "single file diff",
			diff: `diff --git a/internal/rag/rag.go b/internal/rag/rag.go
index 1234567..89abcdef 100644
--- a/internal/rag/rag.go
+++ b/internal/rag/rag.go
@@ -1,3 +1,4 @@
 package rag
 
 import (
+	"fmt"
`,
			want: []internalgithub.ChangedFile{
				{
					Filename: "internal/rag/rag.go",
					Patch: `index 1234567..89abcdef 100644
--- a/internal/rag/rag.go
+++ b/internal/rag/rag.go
 package rag
 
 import (
+	"fmt"
`,
				},
			},
		},
		{
			name: "multiple files diff",
			diff: `diff --git a/file1.go b/file1.go
--- a/file1.go
+++ b/file1.go
@@ -1,1 +1,1 @@
-old
+new
diff --git a/path/to/file2.go b/path/to/file2.go
--- a/path/to/file2.go
+++ b/path/to/file2.go
@@ -10,1 +10,1 @@
-foo
+bar
`,
			want: []internalgithub.ChangedFile{
				{
					Filename: "file1.go",
					Patch: `--- a/file1.go
+++ b/file1.go
-old
+new
`,
				},
				{
					Filename: "path/to/file2.go",
					Patch: `--- a/path/to/file2.go
+++ b/path/to/file2.go
-foo
+bar
`,
				},
			},
		},
		{
			name: "empty diff",
			diff: "",
			want: nil,
		},
		{
			name: "diff with extra junk",
			diff: `some random text
diff --git a/real.go b/real.go
--- a/real.go
+++ b/real.go
@@ -1,1 +1,1 @@
 content
`,
			want: []internalgithub.ChangedFile{
				{
					Filename: "real.go",
					Patch: `--- a/real.go
+++ b/real.go
 content
`,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseDiff(tt.diff); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseDiff() = %v, want %v", got, tt.want)
			}
		})
	}
}
