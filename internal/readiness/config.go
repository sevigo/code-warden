// Package readiness implements the operational readiness review: it detects
// production-facing backend change categories in a PR and investigates each
// applicable category with a focused agent pass, producing readiness-specific
// findings grounded in repository evidence.
package readiness

import (
	"github.com/sevigo/code-warden/internal/core"
)

// Category is a production-facing change class the readiness review covers.
type Category string

const (
	CategoryOutboundHTTP       Category = "outbound_http"
	CategoryBackgroundJob      Category = "background_job"
	CategoryMessaging          Category = "messaging"
	CategoryMigration          Category = "migration"
	CategoryExternalSideEffect Category = "external_side_effect"
)

// Config is the resolved readiness settings for a run, derived from the
// repository's .code-warden.yml.
type Config struct {
	Enabled      bool
	EnabledCats  map[Category]bool
	Context      map[string]string
	Instructions []string
}

// ConfigFromRepo derives a Config from the repository config. It applies the
// repo overrides on top of defaults so partial configs enable only what they
// name.
func ConfigFromRepo(rc *core.RepoConfig) Config {
	if rc == nil {
		return DefaultConfig()
	}
	r := rc.Readiness

	enabled := true
	if r.Enabled != nil {
		enabled = *r.Enabled
	}

	cfg := DefaultConfig()
	cfg.Enabled = enabled
	if r.Context != nil {
		cfg.Context = r.Context
	}
	if r.Instructions != nil {
		cfg.Instructions = r.Instructions
	}

	apply := func(key Category, check *core.CategoryCheck) {
		if check != nil && check.Enabled != nil {
			cfg.EnabledCats[key] = *check.Enabled
		}
	}
	apply(CategoryOutboundHTTP, r.Checks.OutboundHTTP)
	apply(CategoryBackgroundJob, r.Checks.BackgroundJobs)
	apply(CategoryMessaging, r.Checks.Messaging)
	apply(CategoryMigration, r.Checks.Migrations)
	apply(CategoryExternalSideEffect, r.Checks.ExternalSideEffect)

	return cfg
}

// DefaultConfig returns a readiness config with every category enabled and no
// context or instructions.
func DefaultConfig() Config {
	return Config{
		Enabled: true,
		EnabledCats: map[Category]bool{
			CategoryOutboundHTTP:       true,
			CategoryBackgroundJob:      true,
			CategoryMessaging:          true,
			CategoryMigration:          true,
			CategoryExternalSideEffect: true,
		},
		Context: map[string]string{},
	}
}
