package skills

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/sevigo/code-warden/internal/core"
)

// Result is the outcome of running a skill against a change.
type Result struct {
	// Skill is the name of the skill that produced the result.
	Skill string
	// Review is the skill's structured findings. It is non-nil even when a
	// skill finds nothing (an empty review), so callers can distinguish a
	// completed pass from a skipped one.
	Review *core.StructuredReview
}

// Registry is an ordered collection of skills. It owns applicability
// selection: on a change it runs every skill whose Detect matches the changed
// files, unless a command names specific skills to run instead.
type Registry struct {
	skills []Skill
	logger *slog.Logger
}

// NewRegistry builds a registry with the given skills. The order determines the
// order in which matching skills are executed.
func NewRegistry(logger *slog.Logger, skills ...Skill) *Registry {
	if logger == nil {
		logger = slog.Default()
	}
	return &Registry{skills: skills, logger: logger}
}

// Skills returns the registered skills in order.
func (r *Registry) Skills() []Skill {
	out := make([]Skill, len(r.skills))
	copy(out, r.skills)
	return out
}

// Register appends a skill to the registry.
func (r *Registry) Register(skill Skill) {
	r.skills = append(r.skills, skill)
}

// Applicable returns the skills whose Detect matches the changed files.
func (r *Registry) Applicable(changedFiles []core.ChangedFile) []Skill {
	var out []Skill
	for _, skill := range r.skills {
		if skill.Detect(changedFiles) {
			out = append(out, skill)
		}
	}
	return out
}

// Run executes the skills that apply to the change. When overrides is non-empty
// it runs exactly those named skills (in registry order) instead of detecting.
// A missing override name yields an error. Empty results are returned without
// error.
func (r *Registry) Run(ctx context.Context, rc RunContext, overrides []string) ([]Result, error) {
	selected, err := r.selectSkills(rc.ChangedFiles, overrides)
	if err != nil {
		return nil, err
	}

	results := make([]Result, 0, len(selected))
	for _, skill := range selected {
		review, err := skill.Run(ctx, rc)
		if err != nil {
			return nil, fmt.Errorf("skill %s: %w", skill.Name(), err)
		}
		results = append(results, Result{Skill: skill.Name(), Review: review})
	}
	return results, nil
}

// selectSkills resolves which skills to run, honoring command overrides.
func (r *Registry) selectSkills(changedFiles []core.ChangedFile, overrides []string) ([]Skill, error) {
	if len(overrides) == 0 {
		selected := r.Applicable(changedFiles)
		if len(selected) == 0 {
			r.logger.Debug("skills: no skill applies to changed files")
		}
		return selected, nil
	}

	byName := make(map[string]Skill, len(r.skills))
	for _, skill := range r.skills {
		byName[skill.Name()] = skill
	}

	selected := make([]Skill, 0, len(overrides))
	for _, name := range overrides {
		skill, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("skills: unknown skill %q", name)
		}
		selected = append(selected, skill)
	}
	return selected, nil
}
