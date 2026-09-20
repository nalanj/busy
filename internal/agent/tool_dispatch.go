package agent

import (
	"context"
	"fmt"
)

// dispatchTool executes a tool call by name against the registered specs.
// Returns the textual result (the same string we'd put into a sorus.ToolResult).
// Returns an error only for catastrophic failures (unknown tool, internal
// panic); per-tool errors are returned as text so the model can react.
func dispatchTool(ctx context.Context, specs []ToolSpec, name, argsJSON string) (string, error) {
	for _, s := range specs {
		if s.Definition.Name != name {
			continue
		}
		text, err := s.Execute(ctx, argsJSON)
		if err != nil {
			return "", fmt.Errorf("%s: %w", name, err)
		}
		return text, nil
	}
	return "", fmt.Errorf("unknown tool: %s", name)
}
