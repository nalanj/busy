package agent

import (
	"strings"

	"github.com/nalanj/aadc/internal/storage"
	"github.com/nalanj/sorus"
)

// loadMessages reads persisted messages from the store and converts them
// into the sorus form. The on-disk format pre-dates sorus; tool calls are
// stored as "tool" rows with Content = "$ <name> <args>\n<result>". We
// preserve that loss as a single text-blob per past tool message — the
// model treats it as historical conversation context.
//
// System roles are skipped (the system prompt is supplied separately on
// each Request).
func loadMessages(store *storage.Store) ([]sorus.Message, error) {
	stored, err := store.GetMessages()
	if err != nil {
		return nil, err
	}
	out := make([]sorus.Message, 0, len(stored))
	for _, m := range stored {
		if m.Role == string(sorus.RoleSystem) {
			continue
		}
		out = append(out, sorus.Message{
			Role: sorus.Role(m.Role),
			Content: []sorus.Part{
				sorus.Text{Value: m.Content},
			},
		})
	}
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
