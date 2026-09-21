package agent

import (
	"strings"

	"github.com/nalanj/aadc/internal/storage"
	"github.com/nalanj/sorus"
)

// loadMessages reads persisted messages from the store and converts them
// into the sorus form. The on-disk format pre-dates sorus; tool calls are
// stored as "tool" rows with Content = "$ <name> <args>\n<result>".
// Sorus's RoleTool Anthropic builder silently drops non-ToolResult parts,
// which would produce empty messages. We work around that by merging each
// historical tool row's content into the *following* assistant message as
// a Text part, so the model still sees the tool invocation + result in
// its conversation history.
//
// System roles are skipped (the system prompt is supplied separately on
// each Request).
func loadMessages(store *storage.Store) ([]sorus.Message, error) {
	stored, err := store.GetMessages()
	if err != nil {
		return nil, err
	}
	out := make([]sorus.Message, 0, len(stored))
	var pendingTool string
	flush := func() {
		if pendingTool == "" {
			return
		}
		// Find last assistant turn (or user turn if no assistant has been seen yet)
		// and prepend the tool context as a Text part.
		for i := len(out) - 1; i >= 0; i-- {
			if out[i].Role == sorus.RoleAssistant || out[i].Role == sorus.RoleUser {
				prefix := sorus.Text{Value: pendingTool}
				out[i].Content = append([]sorus.Part{prefix}, out[i].Content...)
				pendingTool = ""
				return
			}
		}
		pendingTool = ""
	}
	for _, m := range stored {
		if m.Role == string(sorus.RoleSystem) {
			continue
		}
		if m.Role == "tool" {
			pendingTool = m.Content
			continue
		}
		flush()
		msg := sorus.Message{
			Role: sorus.Role(m.Role),
			Content: []sorus.Part{
				sorus.Text{Value: m.Content},
			},
		}
		out = append(out, msg)
	}
	flush() // any trailing tool row gets dropped (orphaned at end of session)
	return out, nil
}

// saveUserPrompt persists the current turn's user input.
func saveUserPrompt(store *storage.Store, prompt string) error {
	return store.AddMessage(storage.Message{
		Role:    string(sorus.RoleUser),
		Content: prompt,
	})
}

// saveAssistantText persists a plain-text assistant turn.
func saveAssistantText(store *storage.Store, text string) error {
	if text == "" {
		return nil
	}
	return store.AddMessage(storage.Message{
		Role:    string(sorus.RoleAssistant),
		Content: text,
	})
}

// saveToolCall saves an assistant tool call as a "tool" message seeded
// with "$ <name> <args>", matching the historic shape. The matching
// tool-result message is appended by appendToolResult.
func saveToolCall(store *storage.Store, name, argsJSON string) error {
	summary := argsJSON
	if len(summary) > 100 {
		summary = summary[:100] + "..."
	}
	return store.AddMessage(storage.Message{
		Role:     "tool",
		ToolName: name,
		Content:  "$ " + name + " " + summary,
	})
}

// appendToolResult locates the most recent tool message with matching name
// and appends the result on a new line.
func appendToolResult(store *storage.Store, name, result string) error {
	msgs, err := store.GetMessages()
	if err != nil {
		return err
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "tool" && msgs[i].ToolName == name {
			msgs[i].Content = strings.TrimRight(msgs[i].Content, "\n") + "\n" + result
			return store.UpdateMessage(msgs[i])
		}
	}
	// No matching call; record as a standalone tool row.
	return store.AddMessage(storage.Message{
		Role:     "tool",
		ToolName: name,
		Content:  result,
	})
}
