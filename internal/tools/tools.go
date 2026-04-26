package tools

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
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
	readMaxFileSize  = 5 * 1024 * 1024 // 5 MB — refuse to load larger files
	readDefaultLimit = 2000             // lines returned when no limit is given
	readMaxLineLen   = 2000             // chars per line; longer lines are trimmed
	readMaxBytes     = 50 * 1024       // 50 KB — byte budget for read output
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
				Name: "read",
				Description: "Read a text file with line numbers. " +
					"Output is truncated to 2000 lines or 50KB (whichever is hit first). " +
					"Use offset (1-based) and limit to paginate large files. " +
					"Each output line is prefixed with its line number so you can reference exact lines in follow-up edits.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":   map[string]any{"type": "string"},
						"offset": map[string]any{"type": "integer", "minimum": 1, "description": "1-based line number to start reading from"},
						"limit":  map[string]any{"type": "integer", "minimum": 1, "description": "maximum number of lines to return (default 2000)"},
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
				Description: "Execute a bash command in the current working directory. Returns stdout and stderr. Output is truncated to the last 2000 lines or 50KB (whichever is hit first). If truncated, full output is saved to a temp file.",
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
		result, display, err = e.execRead(argumentsJSON)
	case "write":
		result, display, err = e.execWrite(ctx, argumentsJSON)
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
	Offset int    `json:"offset"` // 1-based; 0 means start from line 1
	Limit  int    `json:"limit"`  // 0 means use readDefaultLimit
}

func (e *Executor) execRead(argumentsJSON string) (result, display string, err error) {
	var args readArgs
	if err = json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
		return "", "", fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(args.Path) == "" {
		return "", "", errors.New("path is required")
	}
	path := e.resolvePath(args.Path)

	// ── stat: existence, type, size ──────────────────────────────────────────
	info, statErr := os.Stat(path)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			msg := fmt.Sprintf("file not found: %s", path)
			if suggestions := readSuggestSimilar(path); len(suggestions) > 0 {
				msg += "\n\nDid you mean one of these?\n" + strings.Join(suggestions, "\n")
			}
			return "", "", errors.New(msg)
		}
		return "", "", fmt.Errorf("stat %s: %w", path, statErr)
	}
	if info.IsDir() {
		return "", "", fmt.Errorf("%s is a directory, not a file", path)
	}
	if info.Size() > readMaxFileSize {
		return "", "", fmt.Errorf("file is too large (%d bytes); maximum is %d bytes", info.Size(), readMaxFileSize)
	}

	// ── offset / limit ───────────────────────────────────────────────────────
	// offset is 1-based in the public API (0 and 1 both mean "start at line 1").
	offset := 0
	if args.Offset > 1 {
		offset = args.Offset - 1 // convert to 0-based skip count
	}
	limit := readDefaultLimit
	if args.Limit > 0 {
		limit = args.Limit
	}

	// ── buffered read ─────────────────────────────────────────────────────────
	lines, hasMore, readErr := readFileLines(path, offset, limit, readMaxBytes)
	if readErr != nil {
		return "", "", fmt.Errorf("read %s: %w", path, readErr)
	}

	// ── UTF-8 check ───────────────────────────────────────────────────────────
	for _, l := range lines {
		if !utf8.ValidString(l) {
			return "", "", fmt.Errorf("file contains non-UTF-8 text: %s", path)
		}
	}

	// ── AI result: line-numbered text ─────────────────────────────────────────
	startLine := offset + 1 // 1-based line number of the first returned line
	result = addReadLineNumbers(lines, startLine)
	if hasMore {
		lastLine := startLine + len(lines) - 1
		result += fmt.Sprintf("\n\n[Showing lines %d-%d. Use offset=%d to continue.]",
			startLine, lastLine, lastLine+1)
	}

	// ── TUI display: syntax-highlighted with gutter ───────────────────────────
	display = renderReadDisplay(args.Path, lines, startLine)

	return result, display, nil
}

// readFileLines reads [offset, offset+limit) lines from path using a buffered
// scanner so large files are never fully loaded into memory.
// offset is 0-based. hasMore is true when the file contains more lines beyond
// the returned slice. maxBytes caps cumulative output size; when exceeded the
// last collected line is discarded and hasMore is set to true.
func readFileLines(path string, offset, limit, maxBytes int) (lines []string, hasMore bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// 1 MB scanner buffer handles very long lines (minified JS, base64, etc.).
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	// Skip offset lines.
	for i := 0; i < offset && scanner.Scan(); i++ {}
	if err = scanner.Err(); err != nil {
		return nil, false, err
	}

	// Collect up to limit lines, capping each line's length and total bytes.
	lines = make([]string, 0, limit)
	var totalBytes int
	for len(lines) < limit && scanner.Scan() {
		line := scanner.Text()
		if len(line) > readMaxLineLen {
			line = line[:readMaxLineLen] + "…"
		}
		if totalBytes+len(line) > maxBytes && len(lines) > 0 {
			// Would exceed byte budget — stop before this line.
			hasMore = true
			break
		}
		lines = append(lines, line)
		totalBytes += len(line)
	}

	// If stopped by line limit, peek at one more line.
	if !hasMore && len(lines) == limit && scanner.Scan() {
		hasMore = true
	}

	if err = scanner.Err(); err != nil {
		return nil, false, err
	}
	return lines, hasMore, nil
}

// addReadLineNumbers prefixes each line with a right-aligned line number and a
// │ separator, matching crush's style. startLine is 1-based.
func addReadLineNumbers(lines []string, startLine int) string {
	if len(lines) == 0 {
		return ""
	}
	// Width needed for the largest line number.
	width := len(fmt.Sprintf("%d", startLine+len(lines)-1))
	if width < 4 {
		width = 4
	}
	var b strings.Builder
	for i, l := range lines {
		fmt.Fprintf(&b, "%*d│%s\n", width, startLine+i, l)
	}
	return strings.TrimRight(b.String(), "\n")
}

// readSuggestSimilar looks for files in the same directory whose stem (name
// without extension) contains or is contained by the target stem. Returns up
// to 3 matches as full paths.
func readSuggestSimilar(path string) []string {
	dir := filepath.Dir(path)
	full := strings.ToLower(filepath.Base(path))
	ext := strings.ToLower(filepath.Ext(full))
	stem := strings.TrimSuffix(full, ext) // e.g. "foo" from "foo.go"

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		ename := strings.ToLower(e.Name())
		eext := strings.ToLower(filepath.Ext(ename))
		estem := strings.TrimSuffix(ename, eext)
		// Match on full name or just stem.
		if strings.Contains(ename, full) || strings.Contains(full, ename) ||
			(stem != "" && (strings.Contains(estem, stem) || strings.Contains(stem, estem))) {
			out = append(out, filepath.Join(dir, e.Name()))
			if len(out) >= 3 {
				break
			}
		}
	}
	return out
}

type writeArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (e *Executor) execWrite(ctx context.Context, argumentsJSON string) (result, display string, err error) {
	var args writeArgs
	if err = json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
		return "", "", fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(args.Path) == "" {
		return "", "", errors.New("path is required")
	}
	path := e.resolvePath(args.Path)

	if e.permissions != nil {
		sessionID := permission.SessionIDFromContext(ctx)
		var granted bool
		granted, err = e.permissions.Request(ctx, permission.CreatePermissionRequest{
			SessionID:   sessionID,
			ToolName:    "write",
			Action:      "create",
			Description: fmt.Sprintf("Write %d bytes to %s", len(args.Content), path),
			Path:        path,
			Params:      permission.WriteParams{Path: path, Content: args.Content},
		})
		if err != nil {
			return "", "", fmt.Errorf("%s", permDeniedMessage("write", path, err))
		}
		if !granted {
			return "", "", fmt.Errorf("%s", permDeniedMessage("write", path, nil))
		}
	}

	// Detect new file vs overwrite.
	_, statErr := os.Stat(path)
	isNew := statErr != nil

	if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", "", fmt.Errorf("create parent directories for %s: %w", path, err)
	}
	if err = os.WriteFile(path, []byte(args.Content), 0o644); err != nil {
		return "", "", fmt.Errorf("write %s: %w", path, err)
	}

	lineCount := strings.Count(args.Content, "\n")
	if args.Content != "" && !strings.HasSuffix(args.Content, "\n") {
		lineCount++
	}

	// Model-facing summary.
	if isNew {
		result = fmt.Sprintf("Created %s (%d lines, %d bytes)", path, lineCount, len(args.Content))
	} else {
		result = fmt.Sprintf("Wrote %s (%d lines, %d bytes)", path, lineCount, len(args.Content))
	}

	// TUI display: syntax-highlighted preview with line numbers.
	display = renderWriteDisplay(args.Path, args.Content)

	return result, display, nil
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
			return "", "", fmt.Errorf("%s", permDeniedMessage("edit", path, permErr))
		}
		if !granted {
			return "", "", fmt.Errorf("%s", permDeniedMessage("edit", path, nil))
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
			return "", fmt.Errorf("%s", permDeniedMessage("bash", e.baseDir, err))
		}
		if !granted {
			return "", fmt.Errorf("%s", permDeniedMessage("bash", e.baseDir, nil))
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
	text, wasTruncated := truncateOutputTail(text, e.maxLines, e.maxBytes)
	if wasTruncated {
		tmpPath, tmpErr := writeTruncationTempFile(text)
		if tmpErr == nil {
			text += fmt.Sprintf("\n\n[output truncated. Full output: %s]", tmpPath)
		} else {
			text += "\n\n[output truncated]"
		}
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

// truncateOutputTail keeps the last maxLines lines (up to maxBytes) of input.
// Bash output is most useful at the end — errors, exit codes, final results.
func truncateOutputTail(input string, maxLines, maxBytes int) (string, bool) {
	if maxLines <= 0 {
		maxLines = defaultOutputMaxLines
	}
	if maxBytes <= 0 {
		maxBytes = defaultOutputMaxBytes
	}

	lines := strings.Split(input, "\n")
	if len(lines) <= maxLines && len(input) <= maxBytes {
		return input, false
	}

	// Keep the last maxLines lines.
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	out := strings.Join(lines, "\n")
	if len(out) <= maxBytes {
		return out, true
	}

	// Byte truncation: keep the tail.
	b := []byte(out)
	if len(b) > maxBytes {
		b = b[len(b)-maxBytes:]
	}
	// Align to valid UTF-8 boundary.
	for len(b) > 0 && !utf8.RuneStart(b[0]) {
		b = b[1:]
	}
	return string(b), true
}

// writeTruncationTempFile writes content to a temp file and returns its path.
func writeTruncationTempFile(content string) (string, error) {
	id := make([]byte, 8)
	if _, err := rand.Read(id); err != nil {
		return "", err
	}
	name := fmt.Sprintf("pigeon-bash-%s.log", hex.EncodeToString(id))
	path := filepath.Join(os.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// permDeniedMessage builds a rich error message sent back to the LLM agent
// when a tool call is denied by the user.  It explains what happened and
// suggests alternatives so the agent can recover gracefully.
func permDeniedMessage(tool, path string, err error) string {
	var detail string
	if err != nil {
		detail = fmt.Sprintf(" (%s)", err.Error())
	}
	return fmt.Sprintf(
		"Permission denied: %s on %s was blocked by the user%s. "+
			"Do not retry this exact operation without asking the user first. "+
			"Consider asking the user for guidance or proposing an alternative approach.",
		tool, path, detail,
	)
}
