package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nalanj/sorus"
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
			if !tt.wantError && tt.wantError != (err != nil) {
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
		{"marker on own line", "Hello world\n<<<<<DONE>>>>>\nGoodbye", true},
		{"marker at end", "Hello world\n<<<<<DONE>>>>>", true},
		{"marker only", "<<<<<DONE>>>>>", true},
		{"no marker", "Hello world\nGoodbye", false},
		{"marker with spaces", "Hello\n   <<<<<DONE>>>>>\n", true},
		{"partial marker", "Hello <<<<<DONE>>>>", false},
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

	expected := []string{
		"read_file",
		"edit_file",
		"bash",
		"glob",
		"list_dir",
	}

	if len(tools) != len(expected) {
		t.Fatalf("Expected %d tools, got %d", len(expected), len(tools))
	}
	for i, name := range expected {
		if tools[i].Definition.Name != name {
			t.Errorf("Tool %d: expected %s, got %s", i, name, tools[i].Definition.Name)
		}
	}
}

func TestToolDispatchReadFile(t *testing.T) {
	tmp := t.TempDir()
	path := tmp + "/hello.txt"
	if err := os.WriteFile(path, []byte("hello world"), 0644); err != nil {
		t.Fatal(err)
	}

	specs := StandardTools()
	args := `{"path":"` + path + `"}`
	got, err := dispatchTool(t.Context(), specs, "read_file", args)
	if err != nil {
		t.Fatalf("dispatchTool: %v", err)
	}
	if got != "hello world" {
		t.Errorf("got %q, want %q", got, "hello world")
	}
}

func TestToolDispatchUnknown(t *testing.T) {
	specs := StandardTools()
	_, err := dispatchTool(t.Context(), specs, "no_such_tool", `{}`)
	if err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("expected unknown-tool error, got %v", err)
	}
}

func TestToolDispatchBash(t *testing.T) {
	specs := StandardTools()
	got, err := dispatchTool(t.Context(), specs, "bash", `{"command":"echo hi"}`)
	if err != nil {
		t.Fatalf("dispatchTool: %v", err)
	}
	if strings.TrimSpace(got) != "hi" {
		t.Errorf("got %q, want %q", got, "hi")
	}
}

func TestToolDispatchGlobBothCallShapes(t *testing.T) {
	// Set up a temp dir with two .md files and one .txt file
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "alpha.md"), []byte("a"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "beta.md"), []byte("b"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "ignore.txt"), []byte("c"), 0644); err != nil {
		t.Fatal(err)
	}

	specs := StandardTools()

	// Shape 1: pattern-only with absolute path
	got, err := dispatchTool(t.Context(), specs, "glob", fmt.Sprintf(`{"pattern": %q}`, filepath.Join(tmp, "*.md")))
	if err != nil {
		t.Fatalf("absolute-path pattern: %v", err)
	}
	if strings.Count(got, ".md") != 2 {
		t.Errorf("absolute-path pattern: expected 2 .md matches, got %q", got)
	}

	// Shape 2: dir + relative pattern
	got, err = dispatchTool(t.Context(), specs, "glob", fmt.Sprintf(`{"pattern": "*.md", "dir": %q}`, tmp))
	if err != nil {
		t.Fatalf("dir+pattern: %v", err)
	}
	if strings.Count(got, ".md") != 2 {
		t.Errorf("dir+pattern: expected 2 .md matches, got %q", got)
	}
}

func TestToolDispatchRejectsUnknownFields(t *testing.T) {
	specs := StandardTools()
	// extra field "extra" is unknown to read_fileInput.
	_, err := dispatchTool(t.Context(), specs, "read_file", `{"path":"x","extra":"y"}`)
	if err == nil {
		t.Fatal("expected strict-unmarshal error for unknown field")
	}
}

func TestIsContextTooLarge(t *testing.T) {
	plainErr := errors.New("prompt is too long")
	if !isContextTooLarge(plainErr) {
		t.Error("expected detection of generic 'prompt is too long'")
	}
	otherErr := errors.New("network timeout")
	if isContextTooLarge(otherErr) {
		t.Error("should not flag generic network error")
	}
	openAIErr := errors.New("openai: context_length_exceeded: 4096 > 4096")
	if !isContextTooLarge(openAIErr) {
		t.Error("expected detection of OpenAI context_length_exceeded")
	}
	if isContextTooLarge(nil) {
		t.Error("nil should not flag")
	}
}

func TestSkillsEmptyDir(t *testing.T) {
	skills := Skills("/nonexistent/directory")
	if len(skills) != 0 {
		t.Errorf("Expected 0 skills for nonexistent dir, got %d", len(skills))
	}
}

func TestSkillsLoadFromDir(t *testing.T) {
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
	if section := BuildSkillsSection([]Skill{}); section != "" {
		t.Errorf("Expected empty string for no skills, got: %s", section)
	}
}

func TestBuildSystemPrompt(t *testing.T) {
	tools := ToolsForRequest(StandardTools())
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
	if !strings.Contains(prompt, "<<<<<DONE>>>>>") {
		t.Error("Expected DONE completion instruction in prompt")
	}
}

func TestBuildSystemPromptMinimal(t *testing.T) {
	// Even with empty inputs, the DONE completion instruction is always added.
	if prompt := BuildSystemPrompt("", "", []sorus.Tool{}, []Skill{}, ""); prompt == "" {
		t.Error("Expected DONE instruction even with empty inputs")
	}
	if prompt := BuildSystemPrompt("You are helpful.", "", []sorus.Tool{}, []Skill{}, ""); !strings.Contains(prompt, "You are helpful.") {
		t.Error("Expected body in prompt")
	}
	if prompt := BuildSystemPrompt("You are helpful.", "", []sorus.Tool{}, []Skill{}, ""); !strings.Contains(prompt, "<<<<<DONE>>>>>") {
		t.Error("Expected DONE instruction in prompt")
	}
}

func TestSummarizeArgs(t *testing.T) {
	if got := summarizeArgs(""); got != "{}" {
		t.Errorf("empty: got %q, want %q", got, "{}")
	}
	if got := summarizeArgs(`{"a":1}`); got != `{"a":1}` {
		t.Errorf("short: got %q", got)
	}
	long := strings.Repeat("x", 150)
	got := summarizeArgs(long)
	if !strings.HasSuffix(got, "...") {
		t.Errorf("expected truncation, got %q", got)
	}
	if len(got) > 104 {
		t.Errorf("expected <= 104 chars, got %d", len(got))
	}
}
