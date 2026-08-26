package skills

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sevigo/code-warden/internal/core"
)

type fakeSkill struct {
	name       string
	detect     func([]core.ChangedFile) bool
	review     *core.StructuredReview
	err        error
	calledWith *RunContext
}

func (f *fakeSkill) Name() string        { return f.name }
func (f *fakeSkill) Description() string { return f.name }
func (f *fakeSkill) Mode() Mode          { return ModeAnalyzer }
func (f *fakeSkill) Detect(changed []core.ChangedFile) bool {
	if f.detect == nil {
		return true
	}
	return f.detect(changed)
}
func (f *fakeSkill) Run(_ context.Context, rc RunContext) (*core.StructuredReview, error) {
	f.calledWith = &rc
	return f.review, f.err
}

func TestRegistryApplicable(t *testing.T) {
	r := NewRegistry(nil,
		&fakeSkill{name: "infra", detect: func(c []core.ChangedFile) bool {
			for _, f := range c {
				if f.Filename == "main.tf" {
					return true
				}
			}
			return false
		}},
		&fakeSkill{name: "review", detect: func(_ []core.ChangedFile) bool { return true }},
	)

	app := r.Applicable([]core.ChangedFile{{Filename: "main.tf", Patch: "@@ -1 +1 @@"}})
	assert.Equal(t, []string{"infra", "review"}, names(app))
}

func TestRegistryRunDetectsApplicable(t *testing.T) {
	infra := &fakeSkill{name: "infra", detect: func(_ []core.ChangedFile) bool { return true }, review: &core.StructuredReview{Summary: "infra ok"}}
	review := &fakeSkill{name: "review", review: &core.StructuredReview{Summary: "review ok"}}
	reg := NewRegistry(nil, infra, review)

	results, err := reg.Run(context.Background(), RunContext{ChangedFiles: []core.ChangedFile{{Filename: "main.tf"}}}, nil)
	require.NoError(t, err)
	require.Len(t, results, 2)
	assert.Equal(t, "infra ok", results[0].Review.Summary)
	assert.Equal(t, "review ok", results[1].Review.Summary)
}

func TestRegistryRunHonorsOverrides(t *testing.T) {
	infra := &fakeSkill{name: "infra", review: &core.StructuredReview{Summary: "infra"}}
	review := &fakeSkill{name: "review", review: &core.StructuredReview{Summary: "review"}}
	reg := NewRegistry(nil, infra, review)

	results, err := reg.Run(context.Background(), RunContext{ChangedFiles: []core.ChangedFile{{Filename: "main.go"}}}, []string{"infra"})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "infra", results[0].Skill)
}

func TestRegistryRunUnknownOverride(t *testing.T) {
	reg := NewRegistry(nil, &fakeSkill{name: "infra"})
	_, err := reg.Run(context.Background(), RunContext{}, []string{"nope"})
	require.ErrorContains(t, err, `unknown skill "nope"`)
}

func TestRegistryRunSkillError(t *testing.T) {
	boom := errors.New("boom")
	reg := NewRegistry(nil, &fakeSkill{name: "infra", err: boom})
	_, err := reg.Run(context.Background(), RunContext{ChangedFiles: []core.ChangedFile{{Filename: "main.tf"}}}, nil)
	require.ErrorIs(t, err, boom)
}

func TestParseCommand(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantSkills []string
		wantInstr  string
		wantErr    bool
	}{
		{name: "review", body: "/review", wantSkills: nil, wantInstr: ""},
		{name: "rereview with instructions", body: "/rereview check security", wantSkills: nil, wantInstr: "check security"},
		{name: "empty", body: "", wantErr: true},
		{name: "non-command", body: "just a comment", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			skills, instr, err := ParseCommand(tc.body)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantSkills, skills)
			assert.Equal(t, tc.wantInstr, instr)
		})
	}
}

func names(skills []Skill) []string {
	out := make([]string, 0, len(skills))
	for _, s := range skills {
		out = append(out, s.Name())
	}
	return out
}
