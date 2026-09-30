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

	"io"

	"github.com/nalanj/busy/internal/config"
	"github.com/nalanj/busy/internal/storage"
	"github.com/nalanj/sorus"
)

const doneMarker = "<<<<<DONE>>>>>"
const maxIterations = 100
const maxCompactionRetries = 1

// SSEEmitter is the interface the agent uses to publish live events.
type SSEEmitter interface {
	Emit(eventType string, data any)
}

// LogEntry is a structured log entry emitted to stdout.
type LogEntry struct {
	Time    string `json:"time"`
	Level   string `json:"level"`
	Type    string `json:"type"`
	Message string `json:"message"`
	Tool    string `json:"tool,omitempty"`
	Job     string `json:"job,omitempty"`
}

// Synchronized stdout writer; currentJob gives us log context.
var stdoutMu sync.Mutex
var currentJob string

func SetJob(name string) { currentJob = name }

func logf(level, logType, format string, args ...any) {
	entry := LogEntry{
		Time:    time.Now().Format(time.RFC3339),
		Level:   level,
		Type:    logType,
		Message: fmt.Sprintf(format, args...),
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

// Runner executes one agent against a single sorus client. Each Runner is
// bound to one (provider, model) pair selected at construction.
type Runner struct {
	cfg          *config.AgentConfig
	client       sorus.Client
	model        *sorus.Model
	systemPrompt string
	tools        []ToolSpec
	store        *storage.Store
	sse          SSEEmitter
}

// New loads the sorus catalog, builds a client for the configured provider,
// resolves the model, and constructs a Runner. The agent's workspace is
// created under the state dir.
func New(ctx context.Context, cfg *config.AgentConfig) (*Runner, error) {
	cat, err := sorus.LoadCatalog(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading catalog: %w", err)
	}

	provider := cat.Provider(cfg.Provider)
	if provider == nil {
		return nil, fmt.Errorf("unknown provider: %s", cfg.Provider)
	}

	client, err := sorus.New(ctx, cat, cfg.Provider)
	if err != nil {
		return nil, fmt.Errorf("creating client: %w", err)
	}

	model, err := resolveModel(provider, cfg.Model)
	if err != nil {
		return nil, err
	}

	// Resolve state dir (default ~/.local/share/busy).
	stateDir := cfg.StateDir
	if stateDir == "" {
		home, _ := os.UserHomeDir()
		stateDir = filepath.Join(home, ".local", "share", "busy")
	} else if strings.HasPrefix(stateDir, "~/") {
		home, _ := os.UserHomeDir()
		stateDir = filepath.Join(home, stateDir[2:])
	}

	store, err := storage.NewStore(stateDir)
	if err != nil {
		return nil, fmt.Errorf("creating store: %w", err)
	}

	// Resolve skills dir.
	skillsDir := cfg.SkillsDir
	if skillsDir != "" && strings.HasPrefix(skillsDir, "~/") {
		home, _ := os.UserHomeDir()
		skillsDir = filepath.Join(home, skillsDir[2:])
	}

	// Ensure workspace exists for the system prompt.
	home, _ := os.UserHomeDir()
	workspace := filepath.Join(home, ".local", "share", "busy", cfg.Name, "workspace")
	if err := os.MkdirAll(workspace, 0755); err != nil {
		return nil, fmt.Errorf("creating workspace: %w", err)
	}

	tools := StandardTools()

	systemPrompt := BuildSystemPrompt(
		cfg.System,
		cfg.Name,
		ToolsForRequest(tools),
		Skills(skillsDir),
		workspace,
	)

	return &Runner{
		cfg:          cfg,
		client:       client,
		model:        model,
		systemPrompt: systemPrompt,
		tools:        tools,
		store:        store,
	}, nil
}

// resolveModel finds a model on the provider by case-insensitive ID or name.
// Falls back to the first model if the config didn't specify one.
func resolveModel(p *sorus.Provider, configured string) (*sorus.Model, error) {
	if configured == "" {
		models := p.Models()
		if len(models) == 0 {
			return nil, fmt.Errorf("provider %s has no models", p.ID)
		}
		return models[0], nil
	}
	for _, m := range p.Models() {
		if strings.EqualFold(m.ID, configured) || strings.EqualFold(m.Name, configured) {
			return m, nil
		}
	}
	return nil, fmt.Errorf("provider %s has no model %q", p.ID, configured)
}

func (r *Runner) SetEmitter(emitter SSEEmitter) { r.sse = emitter }
func (r *Runner) Close() error                  { return r.store.Close() }
func (r *Runner) Store() *storage.Store         { return r.store }

// Run executes the agent with the given prompt.
// trigger records what initiated this run (cron expression, @session-start, etc.).
func (r *Runner) Run(ctx context.Context, prompt string, trigger string) error {
	retainTokens := r.cfg.Compaction.GetRetainTokens()

	SetJob(r.cfg.Name)
	logf("info", "agent", "started")

	// Save user prompt.
	msgContent := prompt
	if trigger != "" {
		msgContent = fmt.Sprintf("[%s]\n%s", trigger, prompt)
	}
	if err := saveUserPrompt(r.store, msgContent); err != nil {
		logf("warn", "session", "failed to save user prompt: %v", err)
	}
	if r.sse != nil {
		r.sse.Emit("user", map[string]string{
			"trigger": trigger,
			"content": prompt,
		})
	}

	// Load prior conversation.
	history, err := loadMessages(r.store)
	if err != nil {
		logf("warn", "session", "failed to load previous messages: %v", err)
		history = nil
	} else if len(history) > 0 {
		logf("info", "session", "loaded %d previous messages", len(history))
	}

	messages := append([]sorus.Message(nil), history...)
	compactionRetries := 0

	currentPrompt := prompt + "\n\nWhen finished, respond with '<<<<<DONE>>>>>'."
	initialPrompt := currentPrompt

	for iteration := 0; iteration < maxIterations; iteration++ {
		// Build the next request from scratch each iteration.
		// Sorus's req.Clone() supports this efficiently.
		req := sorus.NewRequest(r.model)
		req.System(r.systemPrompt)
		req.Tools(ToolsForRequest(r.tools)...)
		if budget := int(r.cfg.GetThinkingBudget()); budget > 0 {
			req.Reasoning(sorus.Reasoning{BudgetTokens: budget})
		}
		for _, m := range messages {
			req.Message(m)
		}
		req.User(currentPrompt)

		response, err := r.runStep(ctx, req)
		if err != nil {
			if isContextTooLarge(err) && compactionRetries < maxCompactionRetries {
				logf("info", "compaction", "context too large, compacting")
				summary := r.generateSummary(ctx, messages)
				if cerr := r.store.CompactStart(summary, retainTokens); cerr != nil {
					logf("error", "compaction", "failed: %v", cerr)
				}
				if r.sse != nil {
					r.sse.Emit("compaction", map[string]string{"content": "Context compacted, continuing..."})
				}
				compactionRetries++
				messages = nil
				continue
			}
			logf("error", "agent", "%v", err)
			if r.sse != nil {
				r.sse.Emit("error", map[string]string{"message": err.Error()})
			}
			return fmt.Errorf("agent error: %w", err)
		}

		text := response.Message.Text()

		// Persist assistant text response.
		if err := saveAssistantText(r.store, text); err != nil {
			logf("warn", "session", "failed to save assistant text: %v", err)
		}
		if text != "" && r.sse != nil {
			r.sse.Emit("agent", map[string]string{"content": text})
		}

		// Done marker check.
		if isDone(text) {
			meta, _ := r.store.GetMetadata()
			msgCount := 0
			if meta != nil {
				msgCount = meta.MessageCount
			}
			logf("info", "done", "completed with %d total messages", msgCount)
			if r.sse != nil {
				r.sse.Emit("done", map[string]any{"message": fmt.Sprintf("completed with %d total messages", msgCount)})
			}
			return nil
		}

		// No tool calls → no more work possible.
		if len(response.Message.ToolCalls) == 0 {
			// Model stopped without a done marker and no tool calls.
			logf("warn", "agent", "model stopped without DONE marker (stop_reason=%s)", response.StopReason)
			if r.sse != nil {
				r.sse.Emit("done", map[string]any{"message": "model stopped without completion marker"})
			}
			return nil
		}

		// Append the assistant turn with tool calls to history.
		assistantContent := response.Message.Content
		if len(assistantContent) == 0 && text != "" {
			assistantContent = []sorus.Part{sorus.Text{Value: text}}
		}
		messages = append(messages, sorus.Message{
			Role:      sorus.RoleAssistant,
			Content:   assistantContent,
			ToolCalls: response.Message.ToolCalls,
		})

		// Execute tools and accumulate results.
		var toolParts []sorus.Part
		for _, tc := range response.Message.ToolCalls {
			argsJSON := tc.Arguments
			if argsJSON == "" {
				argsJSON = "{}"
			}
			logTool(tc.Name, summarizeArgs(argsJSON))

			if err := saveToolCall(r.store, tc.Name, argsJSON); err != nil {
				logf("warn", "session", "failed to save tool call: %v", err)
			}
			if r.sse != nil {
				r.sse.Emit("tool", map[string]string{
					"tool":    tc.Name,
					"content": summarizeArgs(argsJSON),
				})
			}

			resultText, execErr := dispatchTool(ctx, r.tools, tc.Name, argsJSON)
			if execErr != nil {
				resultText = "error: " + execErr.Error()
			}
			logf("debug", "tool_result", "%s", resultText)

			if err := appendToolResult(r.store, tc.Name, resultText); err != nil {
				logf("warn", "session", "failed to append tool result: %v", err)
			}

			// For SSE, truncate long results.
			sseResult := resultText
			if len(sseResult) > 200 {
				sseResult = sseResult[:200] + "..."
			}
			if r.sse != nil {
				r.sse.Emit("tool_result", map[string]string{
					"tool":    tc.Name,
					"content": sseResult,
				})
			}

			toolParts = append(toolParts, sorus.ToolResult{
				ToolCallID: tc.ID,
				Content:    []sorus.Part{sorus.Text{Value: resultText}},
			})
		}

		// Append the tool-turn message to history so the next request sees it.
		messages = append(messages, sorus.Message{
			Role:    sorus.RoleTool,
			Content: toolParts,
		})

		// Next iteration prompts the model to continue (with the new tool
		// results already in `messages`).
		currentPrompt = "Continue. When finished, respond with '<<<<<DONE>>>>>'."
		_ = initialPrompt // retained for the very first prompt; unused after iter 0
	}

	logf("error", "agent", "max iterations reached (%d)", maxIterations)
	return fmt.Errorf("max iterations reached (%d)", maxIterations)
}

// runStep performs one streaming call against sorus and returns the final
// Response. Errors from the stream are wrapped and returned; isContextTooLarge
// callers can sniff the underlying SDK error.
func (r *Runner) runStep(ctx context.Context, req *sorus.Request) (*sorus.Response, error) {
	stream, err := r.client.Stream(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("opening stream: %w", err)
	}
	defer stream.Close()

	var (
		content       strings.Builder
		reasoning     strings.Builder
		toolCalls     []sorus.ToolCall
		usage         *sorus.Usage
		stopReason    string
		finalMessage  *sorus.Message
		inThinking    bool
		thinkingChars int
	)

	for stream.Next() {
		ev := stream.Event()
		switch ev.Type {
		case sorus.EventContentStart, sorus.EventReasoningStart:
			// signal — nothing to do.
		case sorus.EventContentDelta:
			content.WriteString(ev.TextDelta)
		case sorus.EventReasoningDelta:
			reasoning.WriteString(ev.ReasoningDelta)
			inThinking = true
		case sorus.EventReasoningEnd:
			if inThinking {
				logf("debug", "thinking", "completed (%d chars)", thinkingChars)
				inThinking = false
				thinkingChars = 0
			}
		case sorus.EventToolCallStart:
			if ev.ToolCall != nil {
				tc := *ev.ToolCall
				tc.Arguments = ""
				toolCalls = append(toolCalls, tc)
			}
		case sorus.EventToolCallDelta:
			if len(toolCalls) > 0 {
				toolCalls[len(toolCalls)-1].Arguments += ev.ArgumentDelta
			}
		case sorus.EventToolCallEnd:
			// assembled tool call is already in toolCalls
		case sorus.EventDone:
			usage = ev.Usage
			stopReason = ev.StopReason
			if ev.Message != nil {
				finalMessage = ev.Message
			}
		case sorus.EventError:
			if ev.Err != nil {
				return nil, ev.Err
			}
		}
	}

	if err := stream.Err(); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}

	if finalMessage == nil {
		// Synthesize from collected fragments (matches sorus's Collect behavior).
		m := sorus.Message{Role: sorus.RoleAssistant}
		if content.Len() > 0 {
			m.Content = append(m.Content, sorus.Text{Value: content.String()})
		}
		if reasoning.Len() > 0 {
			m.Content = append(m.Content, sorus.Text{Value: reasoning.String()})
		}
		if len(toolCalls) > 0 {
			m.ToolCalls = toolCalls
		}
		finalMessage = &m
	}

	resp := &sorus.Response{
		Message:    *finalMessage,
		StopReason: stopReason,
	}
	if usage != nil {
		resp.Usage = *usage
	}
	return resp, nil
}

// generateSummary asks the model for a 1-2 sentence summary of the conversation,
// used by the compaction path. Falls back to a generic message on failure.
func (r *Runner) generateSummary(ctx context.Context, messages []sorus.Message) string {
	if len(messages) == 0 {
		return "Conversation summary unavailable"
	}

	// Build a one-shot summary request — we don't persist it; it only feeds
	// the compaction summary line.
	req := sorus.NewRequest(r.model)
	req.System("You produce concise conversation summaries. Respond with 1-2 sentences.")
	req.User(fmt.Sprintf(
		"Summarize this conversation in 1-2 sentences. There are %d prior messages.",
		len(messages),
	))

	response, err := r.client.Chat(ctx, req)
	if err != nil {
		return "Conversation summary unavailable"
	}

	summary := strings.TrimSpace(response.Message.Text())
	if len(summary) > 200 {
		summary = summary[:200] + "..."
	}
	return summary
}

// summarizeArgs returns a short, single-line summary of a tool call's
// arguments for logging. Falls back to {} on empty input.
func summarizeArgs(s string) string {
	if s == "" {
		return "{}"
	}
	if len(s) > 100 {
		return s[:100] + "..."
	}
	return s
}

func isDone(response string) bool {
	if response == "" {
		return false
	}
	for _, line := range strings.Split(response, "\n") {
		line = strings.TrimSpace(line)
		if line == doneMarker {
			return true
		}
	}
	return strings.HasSuffix(strings.TrimSpace(response), doneMarker)
}

// CheckPreconditions runs shell commands and returns true if all succeed.
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
