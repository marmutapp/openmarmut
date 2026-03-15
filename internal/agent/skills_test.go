package agent

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/marmutapp/openmarmut/internal/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// skillsTestRT is a runtime stub for testing skill loading.
type skillsTestRT struct {
	targetDir string
	files     map[string][]byte
	dirs      map[string][]runtime.FileEntry
}

func (r *skillsTestRT) Init(context.Context) error   { return nil }
func (r *skillsTestRT) Close(context.Context) error   { return nil }
func (r *skillsTestRT) TargetDir() string              { return r.targetDir }
func (r *skillsTestRT) WriteFile(_ context.Context, _ string, _ []byte, _ os.FileMode) error {
	return nil
}
func (r *skillsTestRT) DeleteFile(_ context.Context, _ string) error { return nil }
func (r *skillsTestRT) MkDir(_ context.Context, _ string, _ os.FileMode) error { return nil }
func (r *skillsTestRT) Exec(_ context.Context, _ string, _ runtime.ExecOpts) (*runtime.ExecResult, error) {
	return &runtime.ExecResult{}, nil
}
func (r *skillsTestRT) ListDir(_ context.Context, relPath string) ([]runtime.FileEntry, error) {
	if entries, ok := r.dirs[relPath]; ok {
		return entries, nil
	}
	return nil, fmt.Errorf("directory not found: %s", relPath)
}
func (r *skillsTestRT) ReadFile(_ context.Context, relPath string) ([]byte, error) {
	if data, ok := r.files[relPath]; ok {
		return data, nil
	}
	return nil, os.ErrNotExist
}

func TestLoadSkills_NoDirectory(t *testing.T) {
	rt := &skillsTestRT{
		targetDir: t.TempDir(),
		files:     map[string][]byte{},
		dirs:      map[string][]runtime.FileEntry{},
	}
	skills, err := LoadSkills(context.Background(), rt)
	require.NoError(t, err)
	assert.Empty(t, skills)
}

func TestLoadSkills_WithSkills(t *testing.T) {
	rt := &skillsTestRT{
		targetDir: t.TempDir(),
		files: map[string][]byte{
			".openmarmut/skills/test-gen.md": []byte(`---
description: "Generate comprehensive tests"
trigger: manual
---
# Test Generation
Generate table-driven tests for all functions.`),
			".openmarmut/skills/review.md": []byte(`---
description: "Review code for issues"
trigger: auto
---
# Code Review
Look for bugs and security issues.`),
		},
		dirs: map[string][]runtime.FileEntry{
			".openmarmut/skills": {
				{Name: "test-gen.md"},
				{Name: "review.md"},
			},
		},
	}

	skills, err := LoadSkills(context.Background(), rt)
	require.NoError(t, err)
	require.Len(t, skills, 2)

	testGen := FindSkill(skills, "test-gen")
	require.NotNil(t, testGen)
	assert.Equal(t, "test-gen", testGen.Name)
	assert.Equal(t, "Generate comprehensive tests", testGen.Description)
	assert.Equal(t, "manual", testGen.Trigger)
	assert.Contains(t, testGen.Content, "table-driven tests")

	review := FindSkill(skills, "review")
	require.NotNil(t, review)
	assert.Equal(t, "auto", review.Trigger)
}

func TestLoadSkills_NoFrontmatter(t *testing.T) {
	rt := &skillsTestRT{
		targetDir: t.TempDir(),
		files: map[string][]byte{
			".openmarmut/skills/simple.md": []byte("Just do something."),
		},
		dirs: map[string][]runtime.FileEntry{
			".openmarmut/skills": {
				{Name: "simple.md"},
			},
		},
	}
	skills, err := LoadSkills(context.Background(), rt)
	require.NoError(t, err)
	require.Len(t, skills, 1)
	assert.Equal(t, "simple", skills[0].Name)
	assert.Equal(t, "manual", skills[0].Trigger) // default
	assert.Equal(t, "Just do something.", skills[0].Content)
}

func TestLoadSkills_SkipsNonMd(t *testing.T) {
	rt := &skillsTestRT{
		targetDir: t.TempDir(),
		files: map[string][]byte{
			".openmarmut/skills/notes.txt": []byte("not a skill"),
		},
		dirs: map[string][]runtime.FileEntry{
			".openmarmut/skills": {
				{Name: "notes.txt"},
				{Name: "subdir", IsDir: true},
			},
		},
	}
	skills, err := LoadSkills(context.Background(), rt)
	require.NoError(t, err)
	assert.Empty(t, skills)
}

func TestFindSkill_Found(t *testing.T) {
	skills := []Skill{
		{Name: "test-gen", Description: "Generate tests"},
		{Name: "review", Description: "Code review"},
	}
	s := FindSkill(skills, "test-gen")
	require.NotNil(t, s)
	assert.Equal(t, "test-gen", s.Name)
}

func TestFindSkill_CaseInsensitive(t *testing.T) {
	skills := []Skill{
		{Name: "Test-Gen", Description: "Generate tests"},
	}
	s := FindSkill(skills, "test-gen")
	require.NotNil(t, s)
}

func TestFindSkill_NotFound(t *testing.T) {
	skills := []Skill{
		{Name: "test-gen", Description: "Generate tests"},
	}
	s := FindSkill(skills, "nonexistent")
	assert.Nil(t, s)
}

func TestFormatAutoSkillDescriptions_NoAutoSkills(t *testing.T) {
	skills := []Skill{
		{Name: "manual-skill", Trigger: "manual"},
	}
	result := FormatAutoSkillDescriptions(skills, 1000)
	assert.Empty(t, result)
}

func TestFormatAutoSkillDescriptions_WithAutoSkills(t *testing.T) {
	skills := []Skill{
		{Name: "manual-skill", Trigger: "manual"},
		{Name: "review", Description: "Code review", Trigger: "auto"},
		{Name: "test-gen", Description: "Generate tests", Trigger: "auto"},
	}
	result := FormatAutoSkillDescriptions(skills, 1000)
	assert.Contains(t, result, "## Available Skills")
	assert.Contains(t, result, "- review: Code review")
	assert.Contains(t, result, "- test-gen: Generate tests")
}

func TestFormatAutoSkillDescriptions_Budget(t *testing.T) {
	skills := []Skill{
		{Name: "s1", Description: "description one", Trigger: "auto"},
		{Name: "s2", Description: "description two", Trigger: "auto"},
		{Name: "s3", Description: "description three", Trigger: "auto"},
	}
	// Very small budget — should truncate.
	result := FormatAutoSkillDescriptions(skills, 100)
	assert.Contains(t, result, "more skills available")
}

func TestFormatAutoSkillDescriptions_Empty(t *testing.T) {
	result := FormatAutoSkillDescriptions(nil, 1000)
	assert.Empty(t, result)
}

func TestParseSkill_UnterminatedFrontmatter(t *testing.T) {
	_, err := parseSkill("---\ndescription: broken\n", "test.md")
	assert.Error(t, err)
}
