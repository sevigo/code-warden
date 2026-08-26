package readiness

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sevigo/code-warden/internal/core"
)

func TestConfigFromRepoDefaults(t *testing.T) {
	cfg := ConfigFromRepo(core.DefaultRepoConfig())
	assert.True(t, cfg.Enabled)
	assert.True(t, cfg.EnabledCats[CategoryOutboundHTTP])
	assert.True(t, cfg.EnabledCats[CategoryMigration])
	assert.Empty(t, cfg.Context)
}

func TestConfigFromRepoDisablesCategory(t *testing.T) {
	rc := core.DefaultRepoConfig()
	disabled := false
	rc.Readiness.Checks.OutboundHTTP = &core.CategoryCheck{Enabled: &disabled}

	cfg := ConfigFromRepo(rc)
	assert.False(t, cfg.EnabledCats[CategoryOutboundHTTP])
	assert.True(t, cfg.EnabledCats[CategoryBackgroundJob])
}

func TestConfigFromRepoDisablesAll(t *testing.T) {
	rc := core.DefaultRepoConfig()
	disabled := false
	rc.Readiness.Enabled = &disabled

	cfg := ConfigFromRepo(rc)
	assert.False(t, cfg.Enabled)
}

func TestDefaultDetector(t *testing.T) {
	d := NewDetector()

	t.Run("outbound http", func(t *testing.T) {
		dets := d.Detect([]core.ChangedFile{{Filename: "internal/provider/acme/client.go", Patch: "http.Client"}})
		assertCategory(t, dets, CategoryOutboundHTTP)
	})

	t.Run("migration", func(t *testing.T) {
		dets := d.Detect([]core.ChangedFile{{Filename: "db/migrations/001_add_users.sql", Patch: "ALTER TABLE"}})
		assertCategory(t, dets, CategoryMigration)
	})

	t.Run("background job", func(t *testing.T) {
		dets := d.Detect([]core.ChangedFile{{Filename: "internal/jobs/reconcile.go", Patch: "worker.New"}})
		assertCategory(t, dets, CategoryBackgroundJob)
	})

	t.Run("messaging", func(t *testing.T) {
		dets := d.Detect([]core.ChangedFile{{Filename: "internal/queue/consumer.go", Patch: "kafka.New"}})
		assertCategory(t, dets, CategoryMessaging)
	})

	t.Run("external side effect", func(t *testing.T) {
		dets := d.Detect([]core.ChangedFile{{Filename: "internal/payments/stripe.go", Patch: "stripe.Charge"}})
		assertCategory(t, dets, CategoryExternalSideEffect)
	})

	t.Run("no categories", func(t *testing.T) {
		dets := d.Detect([]core.ChangedFile{{Filename: "internal/http/server.go", Patch: "handler"}})
		assert.Empty(t, dets)
	})
}

func assertCategory(t *testing.T, dets []Detection, cat Category) {
	t.Helper()
	for _, d := range dets {
		if d.Category == cat {
			return
		}
	}
	t.Fatalf("expected category %s, got %v", cat, dets)
}

func TestParseValidLines(t *testing.T) {
	patch := "@@ -1 +1,2 @@\n+a\n+b\n-removed\n context\n"
	lines := parseValidLines(patch)
	assert.Equal(t, map[int]struct{}{1: {}, 2: {}}, lines)
}

func TestDedupe(t *testing.T) {
	s := []core.Suggestion{
		{FilePath: "a.go", LineNumber: 1, RuleID: "x"},
		{FilePath: "a.go", LineNumber: 1, RuleID: "x"},
		{FilePath: "a.go", LineNumber: 2, RuleID: "y"},
	}
	got := dedupe(s)
	require.Len(t, got, 2)
}

func TestVerdictFor(t *testing.T) {
	assert.Equal(t, core.VerdictApprove, verdictFor(nil))
	assert.Equal(t, core.VerdictApprove, verdictFor([]core.Suggestion{{Severity: "Medium"}}))
	assert.Equal(t, core.VerdictRequestChanges, verdictFor([]core.Suggestion{{Severity: "High"}}))
	assert.Equal(t, core.VerdictRequestChanges, verdictFor([]core.Suggestion{{Severity: "Critical"}}))
}
