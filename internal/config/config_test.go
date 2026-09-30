package config

import (
	"os"
	"testing"
)

func TestLoad(t *testing.T) {
	const yaml = `
agent:
  name: test-agent
  provider: anthropic
  model: claude-sonnet-4-20250514
  system: "You are a helpful assistant that responds briefly."
  thinking: low
  listen_addr: "127.0.0.1:8080"
  skills_dir: ~/.config/busy/skills

jobs:
  - name: setup
    schedule: "@session-start"
    prompt: |
      Say hello and then say DONE.
`
	tmpfile, err := os.CreateTemp("", "config-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())

	if _, err := tmpfile.WriteString(yaml); err != nil {
		t.Fatal(err)
	}
	tmpfile.Close()

	cfg, err := Load(tmpfile.Name())
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	if cfg.Agent.Name != "test-agent" {
		t.Errorf("Expected name 'test-agent', got '%s'", cfg.Agent.Name)
	}
	if cfg.Agent.Provider != "anthropic" {
		t.Errorf("Expected provider 'anthropic', got '%s'", cfg.Agent.Provider)
	}
	if cfg.Agent.Model != "claude-sonnet-4-20250514" {
		t.Errorf("Expected model 'claude-sonnet-4-20250514', got '%s'", cfg.Agent.Model)
	}
	if cfg.Agent.Thinking != "low" {
		t.Errorf("Expected thinking 'low', got '%v'", cfg.Agent.Thinking)
	}
	if len(cfg.Jobs) != 1 {
		t.Errorf("Expected 1 job, got %d", len(cfg.Jobs))
	}
	if cfg.Jobs[0].Name != "setup" {
		t.Errorf("Expected job name 'setup', got '%s'", cfg.Jobs[0].Name)
	}
	if cfg.Jobs[0].Schedule != "@session-start" {
		t.Errorf("Expected schedule '@session-start', got '%s'", cfg.Jobs[0].Schedule)
	}
}

func TestThinkingBudget(t *testing.T) {
	tests := []struct {
		thinking  any
		expected  int64
	}{
		{nil, 0},
		{"low", 10000},
		{"medium", 16000},
		{"high", 32000},
		{10000, 10000},
		{float64(5000), 5000},
		{"invalid", 0},
	}

	for _, tt := range tests {
		cfg := &AgentConfig{Thinking: tt.thinking}
		result := cfg.GetThinkingBudget()
		if result != tt.expected {
			t.Errorf("Thinking %v: expected %d, got %d", tt.thinking, tt.expected, result)
		}
	}
}

func TestCompactionRetainTokens(t *testing.T) {
	tests := []struct {
		configured int
		expected   int
	}{
		{0, 20000},    // default
		{-1, 20000},   // below min
		{10000, 10000},
	}

	for _, tt := range tests {
		cfg := &CompactionConfig{RetainTokens: tt.configured}
		result := cfg.GetRetainTokens()
		if result != tt.expected {
			t.Errorf("RetainTokens %d: expected %d, got %d", tt.configured, tt.expected, result)
		}
	}
}

func TestLoadNotFound(t *testing.T) {
	_, err := Load("/nonexistent/file.yaml")
	if err == nil {
		t.Error("Expected error for nonexistent file")
	}
}

func TestLoadInvalidYAML(t *testing.T) {
	// Create temp invalid YAML file
	tmpfile, err := os.CreateTemp("", "invalid-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())

	if _, err := tmpfile.WriteString("invalid: yaml: [[["); err != nil {
		t.Fatal(err)
	}
	tmpfile.Close()

	_, err = Load(tmpfile.Name())
	if err == nil {
		t.Error("Expected error for invalid YAML")
	}
}
