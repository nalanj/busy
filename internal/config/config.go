package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ThinkingBudgets maps named effort levels to token counts
var ThinkingBudgets = map[string]int64{
	"disabled":  0,
	"low":       10000,
	"medium":    16000,
	"high":      32000,
	"very_high": 64000,
	"max":       100000,
}

// Config represents the full agent configuration
type Config struct {
	Agent AgentConfig `yaml:"agent"`
	Jobs  []JobConfig `yaml:"jobs"`
}

// AgentConfig is the agent section of the config
type AgentConfig struct {
	Name        string           `yaml:"name"`
	System      string           `yaml:"system"`
	Provider    string           `yaml:"provider"`
	Model       string           `yaml:"model"`
	Thinking    any              `yaml:"thinking"`
	StateDir    string           `yaml:"state_dir"`
	SkillsDir   string           `yaml:"skills_dir"`
	ListenAddr  string           `yaml:"listen_addr"`
	PathPrefix  string           `yaml:"path_prefix"`
	Compaction  CompactionConfig `yaml:"compaction"`
}

// GetStateDir returns the state directory, defaulting to ~/.local/share/busy
func (a *AgentConfig) GetStateDir() string {
	if a.StateDir != "" {
		return a.StateDir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "busy")
}

// CompactionConfig holds compaction settings
type CompactionConfig struct {
	RetainTokens int `yaml:"retain_tokens"`
}

// GetRetainTokens returns the token budget to retain, defaulting to 20000
func (c *CompactionConfig) GetRetainTokens() int {
	if c.RetainTokens <= 0 {
		return 20000
	}
	return c.RetainTokens
}

// JobConfig represents a single job definition
type JobConfig struct {
	Name         string   `yaml:"name"`
	Schedule     string   `yaml:"schedule"`
	Preconditions []string `yaml:"preconditions"`
	Prompt       string   `yaml:"prompt"`
}

// GetThinkingBudget returns the thinking budget in tokens
func (a *AgentConfig) GetThinkingBudget() int64 {
	if a.Thinking == nil {
		return 0
	}

	switch v := a.Thinking.(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case string:
		if budget, ok := ThinkingBudgets[v]; ok {
			return budget
		}
	}
	return 0
}

// Load reads and parses a YAML config file
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	return &cfg, nil
}
