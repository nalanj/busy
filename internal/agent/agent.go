package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"

	"github.com/nalanj/aadc/internal/provider"

	"github.com/nalanj/aadc/internal/config"
	"github.com/nalanj/aadc/internal/storage"
)

const doneMarker = "<<<<<DONE>>>>>"
const maxIterations = 100
const maxCompactionRetries = 1

// SSEEmitter interface for emitting events to web UI
type SSEEmitter interface {
	Emit(eventType string, data interface{})
}

// LogEntry represents a structured log entry
type LogEntry struct {
	Time    string `json:"time"`
	Level   string `json:"level"`
	Type    string `json:"type"`
	Message string `json:"message"`
	Tool    string `json:"tool,omitempty"`
	Job     string `json:"job,omitempty"`
}

// synchronized stdout writer
var stdoutMu sync.Mutex
var currentJob string

// SetJob sets the current job name for logging context
func SetJob(name string) {
	currentJob = name
}

func log(level, logType, message string) {
	entry := LogEntry{
		Time:    time.Now().Format(time.RFC3339),
		Level:   level,
		Type:    logType,
		Message: message,
	}
	if currentJob != "" {
		entry.Job = currentJob
	}
	data, _ := json.Marshal(entry)
	stdoutMu.Lock()
	defer stdoutMu.Unlock()
	fmt.Println(string(data))
}

func logTool(tool, message string) {
	entry := LogEntry{
		Time:    time.Now().Format(time.RFC3339),
		Level:   "info",
		Type:    "tool",
		Message: message,
		Tool:    tool,
	}
	if currentJob != "" {
		entry.Job = currentJob
	}
	data, _ := json.Marshal(entry)
	stdoutMu.Lock()
	defer stdoutMu.Unlock()
	fmt.Println(string(data))
}

// Runner manages the agent execution
type Runner struct {
	config          *config.AgentConfig
	providerFactory *provider.Factory
	tools          []fantasy.AgentTool
	skills         []Skill
	store          *storage.Store
	sse            SSEEmitter
}

// New creates a new agent runner
func New(cfg *config.AgentConfig, tools []fantasy.AgentTool) (*Runner, error) {
	// Resolve skills directory path
	skillsDir := cfg.SkillsDir
	if skillsDir != "" && strings.HasPrefix(skillsDir, "~/") {
		home, _ := os.UserHomeDir()
		skillsDir = filepath.Join(home, skillsDir[2:])
	}

	// Resolve state directory (default to ~/.local/share/aadc/{agent})
	stateDir := cfg.StateDir
	if stateDir == "" {
		home, _ := os.UserHomeDir()
		stateDir = filepath.Join(home, ".local", "share", "aadc")
	} else if strings.HasPrefix(stateDir, "~/") {
		home, _ := os.UserHomeDir()
		stateDir = filepath.Join(home, stateDir[2:])
	}

	// Create store
	store, err := storage.NewStore(stateDir)
	if err != nil {
		return nil, fmt.Errorf("creating store: %w", err)
	}

	return &Runner{
		config:          cfg,
		providerFactory:  provider.NewFactory(),
		tools:           tools,
		skills:          Skills(skillsDir),
		store:           store,
	}, nil
}

// SetEmitter sets the SSE emitter for web UI events
func (r *Runner) SetEmitter(emitter SSEEmitter) {
	r.sse = emitter
}

// Close closes the runner's resources
func (r *Runner) Close() error {
	return r.store.Close()
}

// Store returns the storage store for the runner
func (r *Runner) Store() *storage.Store {
	return r.store
}

// Run executes the agent with the given prompt
// trigger indicates what initiated this run: @session-start, @web-message, cron expression, etc.
func (r *Runner) Run(ctx context.Context, prompt string, trigger string) error {
	// Get retain tokens from config, default to 20000
	retainTokens := r.config.Compaction.GetRetainTokens()

	// Ensure workspace exists
	home, _ := os.UserHomeDir()
	workspace := filepath.Join(home, ".local", "share", "aadc", r.config.Name, "workspace")
	if err := os.MkdirAll(workspace, 0755); err != nil {
		return fmt.Errorf("creating workspace: %w", err)
	}

	// Build system prompt with skills
	systemPrompt := BuildSystemPrompt(
		r.config.System,
		r.config.Name,
		r.tools,
		r.skills,
		workspace,
	)

	// Create provider
	lm, err := r.createProvider()
	if err != nil {
		return fmt.Errorf("creating provider: %w", err)
	}

	// Build agent options
	agentOptions := []fantasy.AgentOption{
		fantasy.WithSystemPrompt(systemPrompt),
		fantasy.WithTools(r.tools...),
	}

	thinkingBudget := r.config.GetThinkingBudget()
	if thinkingBudget > 0 {
		opts := anthropic.NewProviderOptions(&anthropic.ProviderOptions{
			Thinking: &anthropic.ThinkingProviderOption{
				BudgetTokens: thinkingBudget,
			},
		})
		agentOptions = append(agentOptions, fantasy.WithProviderOptions(opts))
	}

	agentInstance := fantasy.NewAgent(lm, agentOptions...)

	log("info", "agent", "started")
	SetJob(r.config.Name)

	// Load previous messages from session
	messages, err := r.store.GetMessages()
	if err != nil {
		log("warn", "session", fmt.Sprintf("failed to load previous messages: %v", err))
		messages = []storage.Message{}
	} else if len(messages) > 0 {
		log("info", "session", fmt.Sprintf("loaded %d previous messages", len(messages)))
	}

	// Convert stored messages to fantasy format
	fantasyMessages := convertMessages(messages)

	// Wrap initial prompt with completion instructions
	initialPrompt := prompt + "\n\nWhen finished, respond with '<<<<<DONE>>>>>' on its own line."
	currentPrompt := initialPrompt
	iteration := 0
	compactionRetries := 0

	for iteration < maxIterations {
		iteration++

		// Save user prompt to store
		var msgContent string
		if trigger != "" {
			msgContent = fmt.Sprintf("[%s]\n%s", trigger, currentPrompt)
		} else {
			msgContent = currentPrompt
		}
		r.store.AddMessage(storage.Message{
			Role:      "user",
			Content:   msgContent,
			Timestamp: time.Now(),
		})
		if r.sse != nil {
			r.sse.Emit("user", map[string]string{
				"trigger": trigger,
				"content": currentPrompt,
			})
		}

		var textBuilder strings.Builder
		var inThinking bool

		result, err := agentInstance.Stream(ctx, fantasy.AgentStreamCall{
			Prompt:   currentPrompt,
			Messages: fantasyMessages,
			OnTextDelta: func(id, text string) error {
				textBuilder.WriteString(text)
				return nil
			},
			OnReasoningDelta: func(id, text string) error {
				inThinking = true
				return nil
			},
			OnReasoningEnd: func(id string, reasoning fantasy.ReasoningContent) error {
				if inThinking {
					log("debug", "thinking", fmt.Sprintf("completed (%d chars)", len(reasoning.Text)))
					inThinking = false
				}
				return nil
			},
			OnToolCall: func(call fantasy.ToolCallContent) error {
				inputSummary := call.Input
				if len(inputSummary) > 100 {
					inputSummary = inputSummary[:100] + "..."
				}
				logTool(call.ToolName, inputSummary)
				// Store as a single "tool" message; result will be appended to it
				r.store.AddMessage(storage.Message{
					Role:      "tool",
					ToolName:  call.ToolName,
					Content:   "$ " + call.ToolName + " " + inputSummary,
					Timestamp: time.Now(),
				})
				if r.sse != nil {
					r.sse.Emit("tool", map[string]string{
						"tool":    call.ToolName,
						"content": inputSummary,
					})
				}
				return nil
			},
			OnToolResult: func(result fantasy.ToolResultContent) error {
				resultStr := fmt.Sprintf("%v", result.Result)
				if len(resultStr) > 200 {
					resultStr = resultStr[:200] + "..."
				}
				log("debug", "tool_result", resultStr)
				// Find the LAST tool message with matching name and append result
				// This ensures we match the most recent tool call
				msgs, err := r.store.GetMessages()
				if err == nil {
					var lastToolIdx = -1
					for j := len(msgs) - 1; j >= 0; j-- {
						if msgs[j].Role == "tool" && msgs[j].ToolName == result.ToolName {
							lastToolIdx = j
							break
						}
					}
					if lastToolIdx >= 0 {
						msgs[lastToolIdx].Content = msgs[lastToolIdx].Content + "\n" + resultStr
						r.store.UpdateMessage(msgs[lastToolIdx])
					}
				}
				if r.sse != nil {
					r.sse.Emit("tool_result", map[string]string{
						"tool":    result.ToolName,
						"content": resultStr,
					})
				}
				return nil
			},
			OnStepFinish: func(step fantasy.StepResult) error {
				text := textBuilder.String()
				if text != "" {
					r.store.AddMessage(storage.Message{
						Role:      "assistant",
						Content:   text,
						Timestamp: time.Now(),
					})
					if r.sse != nil {
						r.sse.Emit("agent", map[string]string{"content": text})
					}
				}
				return nil
			},
		})

		if err != nil {
			var provErr *fantasy.ProviderError
			if errors.As(err, &provErr) && provErr.IsContextTooLarge() {
				if compactionRetries < maxCompactionRetries {
					log("info", "compaction", "context too large, compacting")
					meta, _ := r.store.GetMetadata()
					msgCount := 0
					if meta != nil {
						msgCount = meta.MessageCount
					}
					summary := generateSummary(ctx, lm, systemPrompt, msgCount)
					if err := r.store.CompactStart(summary, retainTokens); err != nil {
						log("error", "compaction", fmt.Sprintf("failed: %v", err))
					}
					if r.sse != nil {
						r.sse.Emit("compaction", map[string]string{"content": "Context compacted, continuing..."})
					}
					compactionRetries++
					fantasyMessages = nil
					continue
				}
				log("error", "agent", "context overflow")
				if r.sse != nil {
					r.sse.Emit("error", map[string]string{"message": "Context overflow"})
				}
				return fmt.Errorf("context overflow: %w", err)
			}
			log("error", "agent", err.Error())
			if r.sse != nil {
				r.sse.Emit("error", map[string]string{"message": err.Error()})
			}
			return fmt.Errorf("agent error: %w", err)
		}

		// Check for done
		response := textBuilder.String()
		if result != nil && result.Response.Content.Text() != "" {
			response = result.Response.Content.Text()
		}

		if isDone(response) {
			meta, _ := r.store.GetMetadata()
			msgCount := 0
			if meta != nil {
				msgCount = meta.MessageCount
			}
			log("info", "done", fmt.Sprintf("completed with %d total messages", msgCount))
			if r.sse != nil {
				r.sse.Emit("done", map[string]interface{}{"message": fmt.Sprintf("completed with %d total messages", msgCount)})
			}
			return nil
		}

		// Build messages for next iteration
		fantasyMessages = append(fantasyMessages, fantasy.Message{
			Role: fantasy.MessageRoleUser,
			Content: []fantasy.MessagePart{
				&fantasy.TextPart{Text: currentPrompt},
			},
		})
		fantasyMessages = append(fantasyMessages, fantasy.Message{
			Role: fantasy.MessageRoleAssistant,
			Content: []fantasy.MessagePart{
				&fantasy.TextPart{Text: response},
			},
		})

		currentPrompt = "Continue. When finished, respond with '<<<<<DONE>>>>>' on its own line."
	}

	log("error", "agent", fmt.Sprintf("max iterations reached (%d)", maxIterations))
	return fmt.Errorf("max iterations reached")
}

// convertMessages converts storage messages to fantasy messages
func convertMessages(stored []storage.Message) []fantasy.Message {
	var result []fantasy.Message
	for _, msg := range stored {
		role := fantasy.MessageRole(msg.Role)
		// Skip system messages (summary only, actual system prompt handled separately)
		if role == fantasy.MessageRoleSystem {
			continue
		}
		result = append(result, fantasy.Message{
			Role: role,
			Content: []fantasy.MessagePart{
				&fantasy.TextPart{Text: msg.Content},
			},
		})
	}
	return result
}

func isDone(response string) bool {
	for _, line := range strings.Split(response, "\n") {
		line = strings.TrimSpace(line)
		if line == doneMarker {
			return true
		}
	}
	response = strings.TrimSpace(response)
	if strings.HasSuffix(response, doneMarker) {
		return true
	}
	return false
}

// createProvider creates a language model provider
func (r *Runner) createProvider() (fantasy.LanguageModel, error) {
	return r.providerFactory.CreateProvider(r.config.Provider, r.config.Model)
}

// CheckPreconditions runs shell commands and returns true if all succeed
func CheckPreconditions(commands []string) error {
	for _, cmd := range commands {
		cmd = strings.TrimSpace(cmd)
		if cmd == "" {
			continue
		}
		out, err := exec.Command("sh", "-c", cmd).CombinedOutput()
		if err != nil {
			return fmt.Errorf("precondition failed '%s': %s", cmd, string(out))
		}
	}
	return nil
}

// generateSummary creates a summary of the conversation for compaction
func generateSummary(ctx context.Context, lm fantasy.LanguageModel, systemPrompt string, messageCount int) string {
	summaryPrompt := fmt.Sprintf(`Summarize this conversation in 1-2 sentences. The conversation had %d messages. System prompt: %s`, messageCount, systemPrompt)

	response, err := lm.Generate(ctx, fantasy.Call{
		Prompt: []fantasy.Message{
			{Role: fantasy.MessageRoleUser, Content: []fantasy.MessagePart{&fantasy.TextPart{Text: summaryPrompt}}},
		},
	})
	if err != nil {
		return "Conversation summary unavailable"
	}

	summary := strings.TrimSpace(response.Content.Text())
	if len(summary) > 200 {
		summary = summary[:200] + "..."
	}
	return summary
}
