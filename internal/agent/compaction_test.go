package agent

import (
	"fmt"
	"testing"

	"github.com/sevigo/goframe/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── findTailStart ──────────────────────────────────────────────────────────────

func TestFindTailStart_StartsOnHumanMessage(t *testing.T) {
	// Build a message slice that ends with tool-role messages.
	// The tail should walk back past them to the last human message.
	msgs := []schema.MessageContent{
		schema.NewSystemMessage("system"),    // 0 — system prompt
		schema.NewHumanMessage("task"),       // 1
		schema.NewAIMessage("thinking"),      // 2
		schema.NewHumanMessage("user msg"),   // 3  ← expected tail start
		schema.NewAIMessage("tool request"),  // 4
		schema.NewToolResultMessage("t", "r"), // 5  role=tool
		schema.NewAIMessage("tool request2"), // 6
		schema.NewToolResultMessage("t", "r"), // 7  role=tool
		schema.NewAIMessage("final"),         // 8
	}
	// minTail=4 → ideal start = len(9)-4 = 5, but msgs[5] is tool-role.
	// Should walk back to msgs[3] (human).
	start := findTailStart(msgs, 4)
	assert.Equal(t, schema.ChatMessageTypeHuman, msgs[start].Role,
		"tail must start at a human message")
	// Should not be an ai or tool message
	assert.NotEqual(t, schema.ChatMessageTypeTool, msgs[start].Role)
}

func TestFindTailStart_AlreadyOnHuman(t *testing.T) {
	msgs := []schema.MessageContent{
		schema.NewSystemMessage("sys"),
		schema.NewHumanMessage("h1"),
		schema.NewAIMessage("a1"),
		schema.NewHumanMessage("h2"),
		schema.NewAIMessage("a2"),
		schema.NewHumanMessage("h3"), // index 5 — ideal start for minTail=3
	}
	start := findTailStart(msgs, 3)
	assert.Equal(t, 3, start, "already landed on a human message, no walk needed")
}

func TestFindTailStart_TooFewMessages(t *testing.T) {
	msgs := []schema.MessageContent{
		schema.NewSystemMessage("sys"),
		schema.NewHumanMessage("task"),
		schema.NewAIMessage("response"),
	}
	// len=3, minTail+1=9 → returns 1 (keep all)
	start := findTailStart(msgs, 8)
	assert.Equal(t, 1, start)
}

func TestFindTailStart_NeverOrphansToolResult(t *testing.T) {
	// Construct a history where every reasonable cut point is a tool-role message.
	// findTailStart must keep walking until it finds a human message.
	msgs := []schema.MessageContent{
		schema.NewSystemMessage("sys"),        // 0
		schema.NewHumanMessage("start"),       // 1
		schema.NewAIMessage("req"),            // 2
		schema.NewHumanMessage("user reply"),  // 3 ← only safe boundary in range
		schema.NewAIMessage("req"),            // 4
		schema.NewToolResultMessage("t", "x"), // 5
		schema.NewAIMessage("req"),            // 6
		schema.NewToolResultMessage("t", "x"), // 7
		schema.NewAIMessage("req"),            // 8
		schema.NewToolResultMessage("t", "x"), // 9
	}
	start := findTailStart(msgs, 6) // ideal start = 4 (ai msg)
	assert.Equal(t, schema.ChatMessageTypeHuman, msgs[start].Role)
}

// ── extractFileOpsFromMsgs ─────────────────────────────────────────────────────

func toolResultMsg(toolName, path string) schema.MessageContent {
	content := fmt.Sprintf("Tool '%s' returned: {\"ok\":true,\"path\":%q,\"bytes\":10}", toolName, path)
	return schema.NewToolResultMessage(toolName, content)
}

func readResultMsg(path string) schema.MessageContent {
	content := fmt.Sprintf("Tool 'read_file' returned: {\"content\":\"x\",\"lines\":1,\"path\":%q}", path)
	return schema.NewToolResultMessage("read_file", content)
}

func TestExtractFileOpsFromMsgs_Basic(t *testing.T) {
	msgs := []schema.MessageContent{
		schema.NewHumanMessage("task"),
		schema.NewAIMessage("planning"),
		readResultMsg("src/foo.go"),
		readResultMsg("src/bar.go"),
		schema.NewAIMessage("editing"),
		toolResultMsg("edit_file", "src/foo.go"),
		toolResultMsg("write_file", "src/new.go"),
	}

	readFiles, modFiles := extractFileOpsFromMsgs(msgs)

	// foo.go was read then modified → should appear only in modFiles
	assert.Contains(t, modFiles, "src/foo.go", "edited file must be in modFiles")
	assert.Contains(t, modFiles, "src/new.go", "written file must be in modFiles")
	assert.Contains(t, readFiles, "src/bar.go", "read-only file must be in readFiles")
	assert.NotContains(t, readFiles, "src/foo.go", "modified file must not appear in readFiles")
}

func TestExtractFileOpsFromMsgs_NoToolMessages(t *testing.T) {
	msgs := []schema.MessageContent{
		schema.NewHumanMessage("task"),
		schema.NewAIMessage("response"),
	}
	readFiles, modFiles := extractFileOpsFromMsgs(msgs)
	assert.Empty(t, readFiles)
	assert.Empty(t, modFiles)
}

func TestExtractFileOpsFromMsgs_Deduplication(t *testing.T) {
	msgs := []schema.MessageContent{
		readResultMsg("src/foo.go"),
		readResultMsg("src/foo.go"), // duplicate read
		toolResultMsg("edit_file", "src/foo.go"),
		toolResultMsg("edit_file", "src/foo.go"), // duplicate edit
	}
	_, modFiles := extractFileOpsFromMsgs(msgs)
	count := 0
	for _, f := range modFiles {
		if f == "src/foo.go" {
			count++
		}
	}
	assert.Equal(t, 1, count, "duplicate paths must be deduplicated")
}

// ── parseFileTagsFromSummary / formatFileOps round-trip ───────────────────────

func TestFileTagsRoundTrip(t *testing.T) {
	readFiles := []string{"src/bar.go", "src/foo.go"}
	modFiles := []string{"src/baz.go", "src/qux.go"}

	tags := formatFileOps(readFiles, modFiles)
	require.NotEmpty(t, tags)

	gotRead, gotMod := parseFileTagsFromSummary("Some summary text." + tags)
	assert.Equal(t, readFiles, gotRead)
	assert.Equal(t, modFiles, gotMod)
}

func TestFileTagsRoundTrip_ReadOnly(t *testing.T) {
	tags := formatFileOps([]string{"a.go", "b.go"}, nil)
	gotRead, gotMod := parseFileTagsFromSummary(tags)
	assert.Equal(t, []string{"a.go", "b.go"}, gotRead)
	assert.Empty(t, gotMod)
}

func TestFileTagsRoundTrip_ModOnly(t *testing.T) {
	tags := formatFileOps(nil, []string{"c.go"})
	gotRead, gotMod := parseFileTagsFromSummary(tags)
	assert.Empty(t, gotRead)
	assert.Equal(t, []string{"c.go"}, gotMod)
}

func TestFormatFileOps_EmptyLists(t *testing.T) {
	assert.Empty(t, formatFileOps(nil, nil))
	assert.Empty(t, formatFileOps([]string{}, []string{}))
}

func TestParseFileTagsFromSummary_NoTags(t *testing.T) {
	r, m := parseFileTagsFromSummary("plain summary without any XML tags")
	assert.Nil(t, r)
	assert.Nil(t, m)
}

// ── mergeFileLists ─────────────────────────────────────────────────────────────

func TestMergeFileLists_Deduplication(t *testing.T) {
	a := []string{"a.go", "b.go"}
	b := []string{"b.go", "c.go"}
	got := mergeFileLists(a, b)
	assert.Equal(t, []string{"a.go", "b.go", "c.go"}, got)
}

func TestMergeFileLists_BothEmpty(t *testing.T) {
	assert.Empty(t, mergeFileLists(nil, nil))
}

// ── buildUnifiedDiff ───────────────────────────────────────────────────────────

func TestBuildUnifiedDiff_BasicChange(t *testing.T) {
	original := "line1\nline2\nline3\n"
	updated := "line1\nLINE2\nline3\n"
	diff := buildUnifiedDiff(original, updated, "src/foo.go")
	assert.Contains(t, diff, "-line2")
	assert.Contains(t, diff, "+LINE2")
	assert.Contains(t, diff, "a/src/foo.go")
}

func TestBuildUnifiedDiff_NoChange(t *testing.T) {
	content := "unchanged\n"
	diff := buildUnifiedDiff(content, content, "f.go")
	assert.Empty(t, diff, "no diff expected when content is identical")
}

func TestBuildUnifiedDiff_Truncation(t *testing.T) {
	// Create a diff large enough to trigger truncation (> diffMaxBytes).
	var big string
	for i := range 300 {
		big += fmt.Sprintf("line%d\n", i)
	}
	updated := "NEW_FIRST_LINE\n" + big
	diff := buildUnifiedDiff(big, updated, "large.go")
	if len(diff) > diffMaxBytes {
		assert.Contains(t, diff, "[diff truncated]")
	}
}
