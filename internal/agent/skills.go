package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gajaai/openmarmut-go/internal/runtime"
)

// Skill represents a reusable prompt template loaded from .openmarmut/skills/.
type Skill struct {
	Name        string // Derived from filename (without extension).
	Description string // From frontmatter.
	Trigger     string // "manual" or "auto" (default: "manual").
	Content     string // The full skill prompt template.
	Source      string // File path for debugging.
}

// LoadSkills scans .openmarmut/skills/ for .md files and parses them into Skills.
// Returns empty slice (not an error) if the directory doesn't exist.
func LoadSkills(ctx context.Context, rt runtime.Runtime) ([]Skill, error) {
	entries, err := rt.ListDir(ctx, ".openmarmut/skills")
	if err != nil {
		return nil, nil
	}

	var skills []Skill
	for _, e := range entries {
		if e.IsDir || !strings.HasSuffix(e.Name, ".md") {
			continue
		}
		relPath := filepath.Join(".openmarmut/skills", e.Name)
		data, readErr := rt.ReadFile(ctx, relPath)
		if readErr != nil {
			continue
		}

		skill, parseErr := parseSkill(string(data), relPath)
		if parseErr != nil {
			continue
		}
		skills = append(skills, skill)
	}

	return skills, nil
}

// parseSkill extracts frontmatter and content from a skill file.
func parseSkill(content, source string) (Skill, error) {
	// Derive name from filename.
	base := filepath.Base(source)
	name := strings.TrimSuffix(base, filepath.Ext(base))

	skill := Skill{
		Name:    name,
		Trigger: "manual",
		Source:  source,
	}

	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "---") {
		skill.Content = content
		return skill, nil
	}

	endIdx := strings.Index(content[3:], "---")
	if endIdx < 0 {
		return Skill{}, fmt.Errorf("skills.parseSkill(%s): unterminated frontmatter", source)
	}
	frontmatter := content[3 : endIdx+3]
	skill.Content = strings.TrimSpace(content[endIdx+6:])

	// Parse frontmatter fields.
	for _, line := range strings.Split(frontmatter, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "description:") {
			val := strings.TrimSpace(strings.TrimPrefix(trimmed, "description:"))
			val = strings.Trim(val, "\"'")
			skill.Description = val
		} else if strings.HasPrefix(trimmed, "trigger:") {
			val := strings.TrimSpace(strings.TrimPrefix(trimmed, "trigger:"))
			val = strings.Trim(val, "\"'")
			if val == "auto" || val == "manual" {
				skill.Trigger = val
			}
		}
	}

	return skill, nil
}

// FormatAutoSkillDescriptions generates a system prompt section listing
// auto-trigger skills (name + description only, not full content).
// Budget is capped to maxChars to prevent context bloat.
func FormatAutoSkillDescriptions(skills []Skill, maxChars int) string {
	var autoSkills []Skill
	for _, s := range skills {
		if s.Trigger == "auto" {
			autoSkills = append(autoSkills, s)
		}
	}

	if len(autoSkills) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n\n## Available Skills\n")
	sb.WriteString("Use the skill by name when the user's request matches.\n\n")

	totalChars := sb.Len()
	for _, s := range autoSkills {
		line := fmt.Sprintf("- %s: %s\n", s.Name, s.Description)
		if maxChars > 0 && totalChars+len(line) > maxChars {
			sb.WriteString("- (more skills available — use /skill to list all)\n")
			break
		}
		sb.WriteString(line)
		totalChars += len(line)
	}

	return sb.String()
}

// FindSkill returns the skill with the given name, or nil if not found.
func FindSkill(skills []Skill, name string) *Skill {
	name = strings.ToLower(name)
	for i := range skills {
		if strings.ToLower(skills[i].Name) == name {
			return &skills[i]
		}
	}
	return nil
}

// LoadSkillsFromOS loads skills from .openmarmut/skills/ using the OS filesystem.
func LoadSkillsFromOS(targetDir string) ([]Skill, error) {
	skillsDir := filepath.Join(targetDir, ".openmarmut", "skills")
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return nil, nil
	}

	var skills []Skill
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(skillsDir, e.Name())
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		skill, parseErr := parseSkill(string(data), filepath.Join(".openmarmut/skills", e.Name()))
		if parseErr != nil {
			continue
		}
		skills = append(skills, skill)
	}

	return skills, nil
}
