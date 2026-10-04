package agent

import (
	"github.com/nalanj/busy/internal/storage"
	"github.com/nalanj/sorus"
)

// loadMessages reads persisted messages from the store and converts them
// into the sorus form. Tool rows are reconstructed as proper
// sorus.Message{Role: RoleTool, Content: []sorus.Part{ToolResult{...}}}
// — the same shape the agent builds in memory during a live run, and
// the same shape the function-calling API expects.
//
// The previous implementation flattened the tool call and result into a
// single "$ name args\n<result>" text blob and prepended it to the next
// user/assistant message. That put an imitable "$ toolname {args...}"
// pattern in every conversation turn. After enough turns the model
// started writing tool calls as text in its own content field rather
// than as proper API tool_use blocks — the agent would then save the
// fake "File written: ..." confirmation, which the next run would
// re-inject as in-context history, and so on. Storing the call and
// result in separate fields and loading them as proper tool-result
// messages breaks that feedback loop.
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
		if m.Role == "tool" {
			// Old-format rows (saved before the ToolCallID/Args fields
			// existed) have empty ToolCallID. The function-calling API
			// rejects a tool_result whose tool_use_id doesn't match a
			// tool_use in the same request, so we can't safely emit these
			// as proper tool-result messages. Fall back to plain text
			// in the next message instead — the model still sees the
			// result, just not as a structured tool message.
			if m.ToolCallID == "" {
				out = append(out, sorus.Message{
					Role: sorus.RoleUser,
					Content: []sorus.Part{
						sorus.Text{Value: "[historical tool result]\n" + m.Content},
					},
				})
				continue
			}
			// Render as a proper tool-result message. Include the original
			// call args (if stored) so the model can re-derive what call
			// this was a response to — since we don't keep the
			// assistant's tool call alongside the result.
			result := m.Content
			if m.Args != "" {
				result = "Call: " + m.ToolName + "(" + m.Args + ")\nResult: " + m.Content
			}
			out = append(out, sorus.Message{
				Role: sorus.RoleTool,
				Content: []sorus.Part{
					sorus.ToolResult{
						ToolCallID: m.ToolCallID,
						Content: []sorus.Part{
							sorus.Text{Value: result},
						},
					},
				},
			})
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

// saveToolCall saves an assistant tool call as a "tool" row with the
// call ID, tool name, and arguments in structured fields — NOT as a
// "$ <name> <args>" text blob. The matching tool-result row (written
// later by appendToolResult) is keyed by callID.
//
// Using structured fields instead of a text blob prevents the model
// from imitating a "$ toolname args" pattern in its own responses
// (which would prevent it from using the function-calling API).
func saveToolCall(store *storage.Store, name, callID, argsJSON string) error {
	return store.AddMessage(storage.Message{
		Role:       "tool",
		ToolName:   name,
		ToolCallID: callID,
		Args:       argsJSON,
		// Content intentionally left empty; appendToolResult fills it
		// in. (An empty tool row is unusual but valid; the result is
		// required for the model to do anything useful, so we never
		// reach a stable state without one.)
	})
}

// appendToolResult locates the most recent tool row with the given
// callID and sets its Content to the result. Keying by callID (rather
// than tool name) means a single assistant turn can issue two calls
// to the same tool and the results stay attached to the right call.
func appendToolResult(store *storage.Store, callID, result string) error {
	msgs, err := store.GetMessages()
	if err != nil {
		return err
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "tool" && msgs[i].ToolCallID == callID {
			msgs[i].Content = result
			return store.UpdateMessage(msgs[i])
		}
	}
	// No matching call; record as a standalone tool row.
	return store.AddMessage(storage.Message{
		Role:    "tool",
		Content: result,
	})
}
