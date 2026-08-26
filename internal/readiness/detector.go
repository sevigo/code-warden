package readiness

import (
	"strings"

	"github.com/sevigo/code-warden/internal/core"
)

// Evidence anchors one detected change to a specific file.
type Evidence struct {
	// File is the changed file that triggered detection.
	File string
	// Reason is a short human-readable explanation of why this category applies.
	Reason string
}

// Detection is the outcome of detecting one readiness category.
type Detection struct {
	Category Category
	Evidence []Evidence
}

// Detector determines which readiness categories apply to a change, using
// cheap, deterministic heuristics over changed file paths and diff content.
// False negatives are preferred over running every check on every PR.
type Detector interface {
	Detect(changedFiles []core.ChangedFile) []Detection
}

// DefaultDetector implements Detector with file-path and import heuristics.
type DefaultDetector struct{}

// NewDetector returns the default detector.
func NewDetector() *DefaultDetector { return &DefaultDetector{} }

// Detect inspects the changed files and returns every applicable category.
func (d *DefaultDetector) Detect(changedFiles []core.ChangedFile) []Detection {
	var out []Detection
	for _, cat := range []Category{
		CategoryOutboundHTTP,
		CategoryBackgroundJob,
		CategoryMessaging,
		CategoryMigration,
		CategoryExternalSideEffect,
	} {
		if det, ok := d.detectCategory(cat, changedFiles); ok {
			out = append(out, det)
		}
	}
	return out
}

// detectCategory reports whether the category applies and the evidence for it.
func (d *DefaultDetector) detectCategory(cat Category, files []core.ChangedFile) (Detection, bool) {
	var det Detection
	for _, f := range files {
		switch cat {
		case CategoryMigration:
			if isMigrationFile(f.Filename) {
				det.Category = cat
				det.Evidence = append(det.Evidence, Evidence{File: f.Filename, Reason: "migration file changed"})
			}
		case CategoryOutboundHTTP:
			if reason := outboundHTTPReason(f.Filename, f.Patch); reason != "" {
				det.Category = cat
				det.Evidence = append(det.Evidence, Evidence{File: f.Filename, Reason: reason})
			}
		case CategoryBackgroundJob:
			if reason := backgroundJobReason(f.Filename, f.Patch); reason != "" {
				det.Category = cat
				det.Evidence = append(det.Evidence, Evidence{File: f.Filename, Reason: reason})
			}
		case CategoryMessaging:
			if reason := messagingReason(f.Filename, f.Patch); reason != "" {
				det.Category = cat
				det.Evidence = append(det.Evidence, Evidence{File: f.Filename, Reason: reason})
			}
		case CategoryExternalSideEffect:
			if reason := sideEffectReason(f.Filename, f.Patch); reason != "" {
				det.Category = cat
				det.Evidence = append(det.Evidence, Evidence{File: f.Filename, Reason: reason})
			}
		}
	}
	if det.Category == "" {
		return Detection{}, false
	}
	return det, true
}

// isMigrationFile matches SQL migration files under conventional directories.
func isMigrationFile(filename string) bool {
	name := strings.ToLower(filename)
	if !strings.HasSuffix(name, ".sql") {
		return false
	}
	return containsAny(name, "/migrations/", "/migration/", "/db/migrations/", "/sql/migrations/", "schema_")
}

// outboundHTTPReason reports whether the file looks like an outbound HTTP client.
func outboundHTTPReason(filename, patch string) string {
	lower := strings.ToLower(filename)
	switch {
	case containsAny(lower, "/client", "client.", "provider", "/api/", "httpclient", "http_client", "/http/client"):
		return "outbound HTTP/API client file changed"
	case patchContains(patch, `http.Client`, `http.Get`, `http.Post`, `DefaultClient`, `resty`, `go-resty`, "fetch("):
		return "outbound HTTP usage detected"
	}
	return ""
}

// backgroundJobReason reports whether the file looks like a background worker.
func backgroundJobReason(filename, content string) string {
	lower := strings.ToLower(filename)
	switch {
	case containsAny(lower, "/worker", "/workers", "/jobs/", "/job/", "/queue/", "cron", "scheduler", "task"):
		return "background worker/job file changed"
	case patchContains(content, "worker.New", "NewWorker", "cron.New", "time.AfterFunc"):
		return "background worker usage detected"
	}
	return ""
}

// messagingReason reports whether the file uses a message/queue consumer.
func messagingReason(filename, content string) string {
	lower := strings.ToLower(filename)
	switch {
	case containsAny(lower, "consumer", "producer", "publisher", "subscriber", "queue", "message", "listener"):
		return "messaging/queue file changed"
	case patchContains(content,
		"kafka", "rabbitmq", "sqs", "sns", "nats", "pulsar", "amqp", "kafka.New", "sqs.New", "Consumer", "Subscribe"):
		return "messaging/queue usage detected"
	}
	return ""
}

// sideEffectReason reports whether the file involves an external/payment mutation.
func sideEffectReason(filename, content string) string {
	lower := strings.ToLower(filename)
	switch {
	case containsAny(lower, "payment", "billing", "stripe", "invoice", "refund", "charge", "payout", "subscription", "checkout"):
		return "external side effect/payment file changed"
	case patchContains(content,
		"stripe", "charge(", "Capture(", "Refund(", "Payout(", "CancelSubscription", "stripe.Client", "CreateCharge", "chargeIntent"):
		return "external side effect/payment usage detected"
	}
	return ""
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func patchContains(content string, tokens ...string) bool {
	return containsAny(content, tokens...)
}

var _ Detector = (*DefaultDetector)(nil)
