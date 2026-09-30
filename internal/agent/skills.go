package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nalanj/sorus"
)

// Skill represents a loaded skill
type Skill struct {
	Name        string
	Description string
	Location    string // Absolute path to the SKILL.md file
	Content     string // Full skill content (markdown)
}

// Skills returns all available skills from the skills directory
// It scans for subdirectories containing SKILL.md files
func Skills(skillsDir string) []Skill {
	var skills []Skill

	if skillsDir == "" {
		return skills
	}

	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Printf("Warning: failed to read skills dir: %v\n", err)
		}
		return skills
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		skillPath := filepath.Join(skillsDir, entry.Name(), "SKILL.md")
		if _, err := os.Stat(skillPath); os.IsNotExist(err) {
			continue
		}

		skill, err := loadSkill(skillPath)
		if err != nil {
			fmt.Printf("Warning: failed to load skill %s: %v\n", skillPath, err)
			continue
		}

		skills = append(skills, skill)
	}

	return skills
}

// loadSkill loads a single skill from a file
func loadSkill(path string) (Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, err
	}

	content := string(data)

	// Parse frontmatter
	name := extractFrontmatterField(content, "name")
	description := extractFrontmatterField(content, "description")
	body := extractBody(content)

	if name == "" {
		name = strings.TrimSuffix(filepath.Base(path), ".md")
	}
	if description == "" {
		description = "No description"
	}

	return Skill{
		Name:        name,
		Description: description,
		Location:    path,
		Content:     body,
	}, nil
}

// extractFrontmatterField extracts a field value from YAML frontmatter
func extractFrontmatterField(content, field string) string {
	lines := strings.Split(content, "\n")
	inFrontmatter := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if trimmed == "---" {
			if !inFrontmatter {
				inFrontmatter = true
				continue
			}
			break // End of frontmatter
		}

		if inFrontmatter {
			prefix := field + ":"
			if strings.HasPrefix(trimmed, prefix) {
				value := strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
				// Remove quotes if present
				value = strings.Trim(value, "\"")
				return value
			}
		}
	}

	return ""
}

// extractBody extracts the markdown body after frontmatter
func extractBody(content string) string {
	lines := strings.Split(content, "\n")
	inFrontmatter := false
	bodyLines := []string{}
	afterFrontmatter := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if trimmed == "---" {
			if !inFrontmatter {
				inFrontmatter = true
				continue
			}
			afterFrontmatter = true
			continue
		}

		if afterFrontmatter || !inFrontmatter {
			bodyLines = append(bodyLines, line)
		}
	}

	return strings.TrimSpace(strings.Join(bodyLines, "\n"))
}

// BuildSkillsSection creates the skills section for the system prompt
func BuildSkillsSection(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}

	var lines []string
	lines = append(lines, "")
	lines = append(lines, "Here are the skills available to you:")
	lines = append(lines, "")
	lines = append(lines, "Skills contain specialized knowledge and step-by-step workflows for tasks you may not know how to perform from general knowledge alone. Each skill provides detailed instructions, commands, and best practices.")
	lines = append(lines, "")
	lines = append(lines, "<available_skills>")

	for _, skill := range skills {
		lines = append(lines, "  <skill>")
		lines = append(lines, fmt.Sprintf("    <name>%s</name>", skill.Name))
		lines = append(lines, fmt.Sprintf("    <description>%s</description>", skill.Description))
		lines = append(lines, fmt.Sprintf("    <location>%s</location>", skill.Location))
		lines = append(lines, "  </skill>")
	}

	lines = append(lines, "</available_skills>")
	lines = append(lines, "")
	lines = append(lines, "IMPORTANT: When a task matches a skill's purpose, you MUST read the skill file first using the read tool.")

	return strings.Join(lines, "\n")
}

// BuildSystemPrompt composes the full system prompt from agent body, tools, and skills
func BuildSystemPrompt(agentBody string, agentName string, tools []sorus.Tool, skills []Skill, workspacePath string) string {
	var parts []string

	// Agent identity - at the top, prominent
	if agentName != "" {
		parts = append(parts, fmt.Sprintf("You are %s.", agentName))
	}

	// Agent body
	if agentBody != "" {
		parts = append(parts, strings.TrimSpace(agentBody))
	}

	// Workspace guidance
	if workspacePath != "" {
		parts = append(parts, "", fmt.Sprintf("Your workspace folder is %s. Write files there instead of elsewhere.", workspacePath))
	}

	// Tools section
	if len(tools) > 0 {
		parts = append(parts, "", "You have access to these tools:")
		for _, tool := range tools {
			parts = append(parts, "- "+tool.Name+": "+tool.Description)
		}
	}

	// Skills section - at the end
	if len(skills) > 0 {
		parts = append(parts, "", BuildSkillsSection(skills))
	}

	// Completion protocol - applies to every turn
	parts = append(parts, "", "When you have finished responding, output `<<<<<DONE>>>>>` on its own line to signal completion.")

	return strings.Join(parts, "\n")
}
