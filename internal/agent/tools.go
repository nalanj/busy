package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nalanj/sorus"
)

// ToolSpec pairs a sorus.Tool definition with a typed executor. The executor
// unmarshals sorus's JSON args into the typed input and returns a text result.
type ToolSpec struct {
	Definition sorus.Tool
	Execute    func(ctx context.Context, argsJSON string) (string, error)
}

// StandardTools returns the standard tools available to all agents.
func StandardTools() []ToolSpec {
	return []ToolSpec{
		readFileTool(),
		editFileTool(),
		bashTool(),
		globTool(),
		listDirTool(),
	}
}

// ToolsForRequest returns the sorus.Tool definitions (no executors) for
// inclusion on a Request.
func ToolsForRequest(specs []ToolSpec) []sorus.Tool {
	out := make([]sorus.Tool, len(specs))
	for i, s := range specs {
		out[i] = s.Definition
	}
	return out
}

// ---- read_file ----

type readFileInput struct {
	Path string `json:"path" jsonschema:"description=The path of the file to read"`
}

func readFileTool() ToolSpec {
	schema := schemaAsMap(&readFileInput{})
	return ToolSpec{
		Definition: sorus.Tool{
			Name:        "read_file",
			Description: "Read the contents of a file",
			Parameters:  schema,
		},
		Execute: func(_ context.Context, argsJSON string) (string, error) {
			var in readFileInput
			if err := jsonUnmarshalStrict(argsJSON, &in); err != nil {
				return "", err
			}
			data, err := os.ReadFile(in.Path)
			if err != nil {
				return "", err
			}
			return string(data), nil
		},
	}
}

// ---- edit_file ----

type editFileInput struct {
	Path    string `json:"path" jsonschema:"description=The path of the file to edit"`
	Content string `json:"content" jsonschema:"description=The new content for the file"`
	Append  bool   `json:"append,omitempty" jsonschema:"description=If true, append to the file instead of overwriting"`
}

func editFileTool() ToolSpec {
	schema := schemaAsMap(&editFileInput{})
	return ToolSpec{
		Definition: sorus.Tool{
			Name:        "edit_file",
			Description: "Write or append content to a file",
			Parameters:  schema,
		},
		Execute: func(_ context.Context, argsJSON string) (string, error) {
			var in editFileInput
			if err := jsonUnmarshalStrict(argsJSON, &in); err != nil {
				return "", err
			}
			if in.Append {
				f, err := os.OpenFile(in.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
				if err != nil {
					return "", err
				}
				defer f.Close()
				if _, err := f.WriteString(in.Content); err != nil {
					return "", err
				}
				return "Content appended to file: " + in.Path, nil
			}
			if err := os.WriteFile(in.Path, []byte(in.Content), 0644); err != nil {
				return "", err
			}
			return "File written: " + in.Path, nil
		},
	}
}

// ---- bash ----

type bashInput struct {
	Command string `json:"command" jsonschema:"description=The shell command to run"`
	Timeout int    `json:"timeout,omitempty" jsonschema:"description=Timeout in seconds (default 30)"`
}

func bashTool() ToolSpec {
	schema := schemaAsMap(&bashInput{})
	return ToolSpec{
		Definition: sorus.Tool{
			Name:        "bash",
			Description: "Run a shell command",
			Parameters:  schema,
		},
		Execute: func(ctx context.Context, argsJSON string) (string, error) {
			var in bashInput
			if err := jsonUnmarshalStrict(argsJSON, &in); err != nil {
				return "", err
			}
			cmdCtx := ctx
			var cancel context.CancelFunc
			if in.Timeout > 0 {
				cmdCtx, cancel = context.WithTimeout(ctx, time.Duration(in.Timeout)*time.Second)
				defer cancel()
			}
			cmd := exec.CommandContext(cmdCtx, "/bin/sh", "-c", in.Command)
			output, err := cmd.CombinedOutput()
			if err != nil {
				// Include captured output so the model can see why it failed.
				return strings.TrimRight(string(output), "\n") + "\nerror: " + err.Error(), nil
			}
			return strings.TrimRight(string(output), "\n"), nil
		},
	}
}

// ---- glob ----

type globInput struct {
	Pattern string `json:"pattern" jsonschema:"description=Glob pattern to match files (e.g. *.go, src/**/*.ts)"`
	Dir     string `json:"dir,omitempty" jsonschema:"description=Directory to search in (default current directory)"`
}

func globTool() ToolSpec {
	schema := schemaAsMap(&globInput{})
	return ToolSpec{
		Definition: sorus.Tool{
			Name:        "glob",
			Description: "List files matching a glob pattern",
			Parameters:  schema,
		},
		Execute: func(_ context.Context, argsJSON string) (string, error) {
			var in globInput
			if err := jsonUnmarshalStrict(argsJSON, &in); err != nil {
				return "", err
			}
			// If pattern is an absolute path, use it directly — otherwise
			// join dir (default ".") and pattern. This lets callers pass
			// either {pattern: "*.md", dir: "/foo"} or
			// {pattern: "/foo/*.md"}.
			var target string
			if filepath.IsAbs(in.Pattern) {
				target = in.Pattern
			} else {
				dir := "."
				if in.Dir != "" {
					dir = in.Dir
				}
				target = filepath.Join(dir, in.Pattern)
			}
			matches, err := filepath.Glob(target)
			if err != nil {
				return "", err
			}
			if len(matches) == 0 {
				return "No files matched", nil
			}
			return strings.Join(matches, "\n"), nil
		},
	}
}

// ---- list_dir ----

type listDirInput struct {
	Path string `json:"path" jsonschema:"description=The directory path to list"`
}

func listDirTool() ToolSpec {
	schema := schemaAsMap(&listDirInput{})
	return ToolSpec{
		Definition: sorus.Tool{
			Name:        "list_dir",
			Description: "List contents of a directory",
			Parameters:  schema,
		},
		Execute: func(_ context.Context, argsJSON string) (string, error) {
			var in listDirInput
			if err := jsonUnmarshalStrict(argsJSON, &in); err != nil {
				return "", err
			}
			entries, err := os.ReadDir(in.Path)
			if err != nil {
				return "", err
			}
			names := make([]string, len(entries))
			for i, e := range entries {
				names[i] = e.Name()
			}
			return strings.Join(names, "\n"), nil
		},
	}
}
