package agent

import (
	"os"
	"strings"
	"testing"

	"charm.land/fantasy"
)

func TestCheckPreconditions(t *testing.T) {
	tests := []struct {
		name      string
		commands  []string
		wantError bool
	}{
		{
			name:      "empty",
			commands:  []string{},
			wantError: false,
		},
		{
			name:      "empty string",
			commands:  []string{"", "  "},
			wantError: false,
		},
		{
			name:      "successful command",
			commands:  []string{"echo hello"},
			wantError: false,
		},
		{
			name:      "successful command with whitespace",
			commands:  []string{"  echo hello  "},
			wantError: false,
		},
		{
			name:      "failing command",
			commands:  []string{"false"},
			wantError: true,
		},
		{
			name:      "multiple commands, one fails",
			commands:  []string{"echo hello", "false", "echo world"},
			wantError: true,
		},
		{
			name:      "multiple commands, all pass",
			commands:  []string{"echo hello", "echo world"},
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckPreconditions(tt.commands)
			if tt.wantError && err == nil {
				t.Error("Expected error but got none")
			}
			if !tt.wantError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestIsDone(t *testing.T) {
	tests := []struct {
		name     string
		response string
		want     bool
	}{
		{
			name:     "marker on own line",
			response: "Hello world\n<<<<<DONE>>>>>\nGoodbye",
			want:     true,
		},
		{
			name:     "marker at end",
			response: "Hello world\n<<<<<DONE>>>>>",
			want:     true,
		},
		{
			name:     "marker only",
			response: "<<<<<DONE>>>>>",
			want:     true,
		},
		{
			name:     "no marker",
			response: "Hello world\nGoodbye",
			want:     false,
		},
		{
			name:     "marker with spaces",
			response: "Hello\n   <<<<<DONE>>>>>\n",
			want:     true,
		},
		{
			name:     "partial marker",
			response: "Hello <<<<<DONE>>>>",
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isDone(tt.response)
			if got != tt.want {
				t.Errorf("isDone(%q) = %v, want %v", tt.response, got, tt.want)
			}
		})
	}
}

func TestStandardTools(t *testing.T) {
	tools := StandardTools()

	expectedTools := []string{
		"read_file",
		"edit_file",
		"bash",
		"glob",
		"list_dir",
	}

	if len(tools) != len(expectedTools) {
		t.Errorf("Expected %d tools, got %d", len(expectedTools), len(tools))
	}

	for i, expected := range expectedTools {
		if tools[i].Info().Name != expected {
			t.Errorf("Tool %d: expected %s, got %s", i, expected, tools[i].Info().Name)
		}
	}
}

func TestSkillsEmptyDir(t *testing.T) {
	skills := Skills("/nonexistent/directory")
	if len(skills) != 0 {
		t.Errorf("Expected 0 skills for nonexistent dir, got %d", len(skills))
	}
}

func TestSkillsLoadFromDir(t *testing.T) {
	// Create a temp skill directory
	tmpDir := t.TempDir()
	skillDir := tmpDir + "/test-skill"
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}

	skillContent := `---
name: test-skill
description: A test skill
---

# Test Skill Content

This skill does something.`

	if err := os.WriteFile(skillDir+"/SKILL.md", []byte(skillContent), 0644); err != nil {
		t.Fatal(err)
	}

	skills := Skills(tmpDir)
	if len(skills) != 1 {
		t.Fatalf("Expected 1 skill, got %d", len(skills))
	}

	if skills[0].Name != "test-skill" {
		t.Errorf("Expected name 'test-skill', got '%s'", skills[0].Name)
	}
	if skills[0].Description != "A test skill" {
		t.Errorf("Expected description 'A test skill', got '%s'", skills[0].Description)
	}
	if !strings.Contains(skills[0].Content, "Test Skill Content") {
		t.Error("Expected skill body content")
	}
}

func TestBuildSkillsSection(t *testing.T) {
	skills := []Skill{
		{Name: "test-skill", Description: "A test skill", Location: "/path/to/skills/test-skill/SKILL.md"},
	}

	section := BuildSkillsSection(skills)

	if !strings.Contains(section, "<available_skills>") {
		t.Error("Expected available_skills tag")
	}
	if !strings.Contains(section, "test-skill") {
		t.Error("Expected skill name in section")
	}
	if !strings.Contains(section, "IMPORTANT") {
		t.Error("Expected IMPORTANT instruction")
	}
}

func TestBuildSkillsSectionEmpty(t *testing.T) {
	section := BuildSkillsSection([]Skill{})
	if section != "" {
		t.Errorf("Expected empty string for no skills, got: %s", section)
	}
}

func TestBuildSystemPrompt(t *testing.T) {
	tools := StandardTools()
	skills := []Skill{
		{Name: "test-skill", Description: "A test skill", Location: "/path/skills/test/SKILL.md"},
	}

	prompt := BuildSystemPrompt("You are a test agent.", "test-agent", tools, skills, "/workspace")

	if !strings.Contains(prompt, "You are test-agent.") {
		t.Error("Expected agent name in prompt")
	}
	if !strings.Contains(prompt, "You are a test agent.") {
		t.Error("Expected agent body in prompt")
	}
	if !strings.Contains(prompt, "read_file") {
		t.Error("Expected tool in prompt")
	}
	if !strings.Contains(prompt, "test-skill") {
		t.Error("Expected skill in prompt")
	}
	if !strings.Contains(prompt, "/workspace") {
		t.Error("Expected workspace in prompt")
	}
}

func TestBuildSystemPromptMinimal(t *testing.T) {
	// Empty agent name results in empty prompt
	prompt := BuildSystemPrompt("", "", []fantasy.AgentTool{}, []Skill{}, "")
	if prompt != "" {
		t.Errorf("Expected empty prompt for empty agent name, got: %s", prompt)
	}

	// Non-empty body still produces prompt
	prompt = BuildSystemPrompt("You are helpful.", "", []fantasy.AgentTool{}, []Skill{}, "")
	if !strings.Contains(prompt, "You are helpful.") {
		t.Error("Expected body in prompt")
	}
}
