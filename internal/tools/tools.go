package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"pigeon/internal/permission"
	"pigeon/internal/provider/openrouter"
)

const (
	defaultOutputMaxLines = 2000
	defaultOutputMaxBytes = 50 * 1024
)

type Executor struct {
	baseDir     string
	maxLines    int
	maxBytes    int
	permissions permission.Service // nil = no permission checks
}

// NewExecutor creates an executor with no permission checks.
func NewExecutor() *Executor {
	wd, err := os.Getwd()
	if err != nil {
		wd = "."
	}
	return &Executor{
		baseDir:  wd,
		maxLines: defaultOutputMaxLines,
		maxBytes: defaultOutputMaxBytes,
	}
}

// NewExecutorWithPermissions creates an executor that checks permissions before
// running bash, write, or edit operations.
func NewExecutorWithPermissions(perm permission.Service) *Executor {
	e := NewExecutor()
	e.permissions = perm
	return e
}

func (e *Executor) Definitions() []openrouter.ToolDefinition {
	return []openrouter.ToolDefinition{
		{
			Type: "function",
			Function: openrouter.ToolFunctionDefinition{
				Name:        "read",
				Description: "Read a text file. Supports optional offset/limit line slicing.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{"type": "string"},
						"offset": map[string]any{"type": "integer", "minimum": 1},
						"limit": map[string]any{"type": "integer", "minimum": 1},
					},
					"required": []string{"path"},
				},
			},
		},
		{
			Type: "function",
			Function: openrouter.ToolFunctionDefinition{
				Name:        "write",
				Description: "Write content to a file, creating parent directories if needed.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{"type": "string"},
						"content": map[string]any{"type": "string"},
					},
					"required": []string{"path", "content"},
				},
			},
		},
		{
			Type: "function",
			Function: openrouter.ToolFunctionDefinition{
				Name:        "edit",
				Description: "Replace one exact text fragment in a file.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{"type": "string"},
						"oldText": map[string]any{"type": "string"},
						"newText": map[string]any{"type": "string"},
					},
					"required": []string{"path", "oldText", "newText"},
				},
			},
		},
		{
			Type: "function",
			Function: openrouter.ToolFunctionDefinition{
				Name:        "bash",
				Description: "Execute a bash command in the current working directory.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"command": map[string]any{"type": "string"},
						"timeout": map[string]any{"type": "integer", "minimum": 1},
					},
					"required": []string{"command"},
				},
			},
		},
	}
}

// Execute runs the named tool and returns (result, display, error).
// result is the plain-text string sent back to the model.
// display is an optional ANSI-colourised string shown in the TUI; when empty
// the TUI falls back to result.
func (e *Executor) Execute(ctx context.Context, name, argumentsJSON string) (result, display string, err error) {
	switch strings.TrimSpace(name) {
	case "read":
		result, err = e.execRead(argumentsJSON)
	case "write":
		result, err = e.execWrite(ctx, argumentsJSON)
	case "edit":
		result, display, err = e.execEdit(ctx, argumentsJSON)
	case "bash":
		result, err = e.execBash(ctx, argumentsJSON)
	default:
		err = fmt.Errorf("unknown tool: %s", name)
	}
	return
}

type readArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

func (e *Executor) execRead(argumentsJSON string) (string, error) { //nolint:unparam
	var args readArgs
	if err := json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(args.Path) == "" {
		return "", errors.New("path is required")
	}
	path := e.resolvePath(args.Path)

	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	text := string(data)
	if !utf8.Valid(data) {
		return "", fmt.Errorf("file is not valid utf-8 text: %s", path)
	}

	lines := strings.Split(text, "\n")
	start := 0
	if args.Offset > 0 {
		start = args.Offset - 1
	}
	if start > len(lines) {
		start = len(lines)
	}
	end := len(lines)
	if args.Limit > 0 && start+args.Limit < end {
		end = start + args.Limit
	}
	selected := strings.Join(lines[start:end], "\n")

	truncated, wasTruncated := truncateOutput(selected, e.maxLines, e.maxBytes)
	if wasTruncated {
		return truncated + "\n\n[output truncated]", nil
	}
	return truncated, nil
}

type writeArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (e *Executor) execWrite(ctx context.Context, argumentsJSON string) (string, error) {
	var args writeArgs
	if err := json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(args.Path) == "" {
		return "", errors.New("path is required")
	}
	path := e.resolvePath(args.Path)

	if e.permissions != nil {
		sessionID := permission.SessionIDFromContext(ctx)
		granted, err := e.permissions.Request(ctx, permission.CreatePermissionRequest{
			SessionID:   sessionID,
			ToolName:    "write",
			Action:      "create",
			Description: fmt.Sprintf("Write %d bytes to %s", len(args.Content), path),
			Path:        path,
			Params:      permission.WriteParams{Path: path, Content: args.Content},
		})
		if err != nil {
			return "", fmt.Errorf("permission denied: %w", err)
		}
		if !granted {
			return "", errors.New("permission denied")
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create parent directories for %s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(args.Content), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return fmt.Sprintf("Wrote %d bytes to %s", len(args.Content), path), nil
}

type editArgs struct {
	Path    string `json:"path"`
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}

func (e *Executor) execEdit(ctx context.Context, argumentsJSON string) (result, display string, err error) {
	var args editArgs
	if err = json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
		return "", "", fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(args.Path) == "" {
		return "", "", errors.New("path is required")
	}
	path := e.resolvePath(args.Path)

	if e.permissions != nil {
		sessionID := permission.SessionIDFromContext(ctx)
		granted, permErr := e.permissions.Request(ctx, permission.CreatePermissionRequest{
			SessionID:   sessionID,
			ToolName:    "edit",
			Action:      "modify",
			Description: fmt.Sprintf("Edit %s", path),
			Path:        path,
			Params:      permission.EditParams{Path: path, OldText: args.OldText, NewText: args.NewText},
		})
		if permErr != nil {
			return "", "", fmt.Errorf("permission denied: %w", permErr)
		}
		if !granted {
			return "", "", errors.New("permission denied")
		}
	}

	data, readErr := os.ReadFile(path)
	if readErr != nil {
		return "", "", fmt.Errorf("read %s: %w", path, readErr)
	}
	content := string(data)
	count := strings.Count(content, args.OldText)
	if count == 0 {
		return "", "", errors.New("oldText not found")
	}
	if count > 1 {
		return "", "", errors.New("oldText matched multiple locations; edit is ambiguous")
	}
	updated := strings.Replace(content, args.OldText, args.NewText, 1)
	if writeErr := os.WriteFile(path, []byte(updated), 0o644); writeErr != nil {
		return "", "", fmt.Errorf("write %s: %w", path, writeErr)
	}

	// Build a colorised diff for the TUI and a short summary for the model.
	diff := buildEditDiff(args.Path, content, updated)
	return diff.summary, diff.display, nil
}

type bashArgs struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

func (e *Executor) execBash(ctx context.Context, argumentsJSON string) (string, error) {
	var args bashArgs
	if err := json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(args.Command) == "" {
		return "", errors.New("command is required")
	}

	if e.permissions != nil {
		sessionID := permission.SessionIDFromContext(ctx)
		granted, err := e.permissions.Request(ctx, permission.CreatePermissionRequest{
			SessionID:   sessionID,
			ToolName:    "bash",
			Action:      "execute",
			Description: args.Command,
			Path:        e.baseDir,
			Params:      permission.BashParams{Command: args.Command},
		})
		if err != nil {
			return "", fmt.Errorf("permission denied: %w", err)
		}
		if !granted {
			return "", errors.New("permission denied")
		}
	}

	runCtx := ctx
	cancel := func() {}
	if args.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(args.Timeout)*time.Second)
	}
	defer cancel()

	cmd := exec.CommandContext(runCtx, "bash", "-lc", args.Command)
	cmd.Dir = e.baseDir
	out, err := cmd.CombinedOutput()

	text := string(out)
	if strings.TrimSpace(text) == "" {
		text = "(no output)"
	}
	text, wasTruncated := truncateOutput(text, e.maxLines, e.maxBytes)
	if wasTruncated {
		text += "\n\n[output truncated]"
	}

	if err != nil {
		if runCtx.Err() == context.DeadlineExceeded {
			return text, fmt.Errorf("command timed out")
		}
		return text, fmt.Errorf("command failed: %w", err)
	}
	return text, nil
}

func (e *Executor) resolvePath(path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(e.baseDir, path)
}

func truncateOutput(input string, maxLines, maxBytes int) (string, bool) {
	if maxLines <= 0 {
		maxLines = defaultOutputMaxLines
	}
	if maxBytes <= 0 {
		maxBytes = defaultOutputMaxBytes
	}

	lines := strings.Split(input, "\n")
	truncated := false
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		truncated = true
	}
	out := strings.Join(lines, "\n")
	if len(out) <= maxBytes {
		return out, truncated
	}
	truncated = true
	b := []byte(out)
	if len(b) > maxBytes {
		b = b[:maxBytes]
	}
	for !utf8.Valid(b) && len(b) > 0 {
		b = b[:len(b)-1]
	}
	return string(b), truncated
}
