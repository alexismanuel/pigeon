package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecutorReadWriteEdit(t *testing.T) {
	tmp := t.TempDir()
	e := &Executor{baseDir: tmp, maxLines: 2000, maxBytes: 50 * 1024}

	writeArgs, _ := json.Marshal(map[string]any{"path": "a/b.txt", "content": "hello\nworld"})
	if _, _, err := e.Execute(context.Background(), "write", string(writeArgs)); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	readArgs, _ := json.Marshal(map[string]any{"path": "a/b.txt"})
	out, _, err := e.Execute(context.Background(), "read", string(readArgs))
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	// Output is now line-numbered: "   1│hello\n   2│world"
	if !strings.Contains(out, "hello") || !strings.Contains(out, "world") {
		t.Fatalf("unexpected read output: %q", out)
	}

	editArgs, _ := json.Marshal(map[string]any{"path": "a/b.txt", "oldText": "world", "newText": "pigeon"})
	if _, _, err := e.Execute(context.Background(), "edit", string(editArgs)); err != nil {
		t.Fatalf("edit failed: %v", err)
	}

	out, _, err = e.Execute(context.Background(), "read", string(readArgs))
	if err != nil {
		t.Fatalf("read after edit failed: %v", err)
	}
	if !strings.Contains(out, "pigeon") || strings.Contains(out, "world") {
		t.Fatalf("unexpected content after edit: %q", out)
	}

	absPath := filepath.Join(tmp, "a", "b.txt")
	absReadArgs, _ := json.Marshal(map[string]any{"path": absPath})
	out, _, err = e.Execute(context.Background(), "read", string(absReadArgs))
	if err != nil {
		t.Fatalf("read absolute path failed: %v", err)
	}
	if !strings.Contains(out, "pigeon") {
		t.Fatalf("unexpected absolute read content: %q", out)
	}
}

func TestExecutorBashAndTimeout(t *testing.T) {
	tmp := t.TempDir()
	e := &Executor{baseDir: tmp, maxLines: 2000, maxBytes: 50 * 1024}

	okArgs, _ := json.Marshal(map[string]any{"command": "echo hi"})
	out, _, err := e.Execute(context.Background(), "bash", string(okArgs))
	if err != nil {
		t.Fatalf("bash echo failed: %v", err)
	}
	if !strings.Contains(out, "hi") {
		t.Fatalf("unexpected bash output: %q", out)
	}

	timeoutArgs, _ := json.Marshal(map[string]any{"command": "sleep 2", "timeout": 1})
	_, _, err = e.Execute(context.Background(), "bash", string(timeoutArgs))
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got: %v", err)
	}
}

func TestTruncateOutputTail(t *testing.T) {
	in := "1\n2\n3\n4"
	out, truncated := truncateOutputTail(in, 2, 100)
	if !truncated {
		t.Fatalf("expected truncated=true")
	}
	if out != "3\n4" {
		t.Fatalf("unexpected output: %q", out)
	}

	out, truncated = truncateOutputTail("abcdef", 100, 3)
	if !truncated || out != "def" {
		t.Fatalf("unexpected byte truncation: out=%q truncated=%v", out, truncated)
	}
}

func TestNewExecutor_Defaults(t *testing.T) {
	e := NewExecutor()
	defs := e.Definitions()
	names := map[string]bool{}
	for _, d := range defs {
		names[d.Function.Name] = true
	}
	for _, want := range []string{"read", "write", "edit", "bash"} {
		if !names[want] {
			t.Errorf("missing tool definition: %s", want)
		}
	}
}

func TestExecutorRead_Offset(t *testing.T) {
	tmp := t.TempDir()
	e := &Executor{baseDir: tmp, maxLines: 2000, maxBytes: 50 * 1024}

	wArgs, _ := json.Marshal(map[string]any{"path": "f.txt", "content": "a\nb\nc\nd"})
	e.Execute(context.Background(), "write", string(wArgs))

	// offset=2, limit=2 → should return lines 2 and 3 ("b" and "c"), not "a" or "d".
	rArgs, _ := json.Marshal(map[string]any{"path": "f.txt", "offset": 2, "limit": 2})
	out, _, err := e.Execute(context.Background(), "read", string(rArgs))
	if err != nil {
		t.Fatalf("read with offset: %v", err)
	}
	if !strings.Contains(out, "b") || !strings.Contains(out, "c") {
		t.Errorf("offset/limit not applied: %q", out)
	}
	if strings.Contains(out, "│a") || strings.Contains(out, "│d") {
		t.Errorf("offset/limit returned out-of-range lines: %q", out)
	}
}

func TestExecutorWrite_CreatesDirectories(t *testing.T) {
	tmp := t.TempDir()
	e := &Executor{baseDir: tmp, maxLines: 2000, maxBytes: 50 * 1024}

	wArgs, _ := json.Marshal(map[string]any{"path": "deep/nested/file.txt", "content": "hi"})
	if _, _, err := e.Execute(context.Background(), "write", string(wArgs)); err != nil {
		t.Fatalf("write nested path: %v", err)
	}
}

func TestExecutorEdit_OldTextNotFound(t *testing.T) {
	tmp := t.TempDir()
	e := &Executor{baseDir: tmp, maxLines: 2000, maxBytes: 50 * 1024}

	wArgs, _ := json.Marshal(map[string]any{"path": "f.txt", "content": "hello"})
	e.Execute(context.Background(), "write", string(wArgs))

	eArgs, _ := json.Marshal(map[string]any{"path": "f.txt", "oldText": "nothere", "newText": "x"})
	_, _, err := e.Execute(context.Background(), "edit", string(eArgs))
	if err == nil {
		t.Error("expected error when oldText not found")
	}
}

func TestExecutorBash_EnvAndWorkDir(t *testing.T) {
	tmp := t.TempDir()
	e := &Executor{baseDir: tmp, maxLines: 2000, maxBytes: 50 * 1024}

	args, _ := json.Marshal(map[string]any{"command": "pwd"})
	out, _, err := e.Execute(context.Background(), "bash", string(args))
	if err != nil {
		t.Fatalf("pwd failed: %v", err)
	}
	if strings.TrimSpace(out) == "" {
		t.Error("expected pwd output")
	}
}

func TestExecutorUnknownTool(t *testing.T) {
	tmp := t.TempDir()
	e := &Executor{baseDir: tmp, maxLines: 2000, maxBytes: 50 * 1024}
	_, _, err := e.Execute(context.Background(), "nonexistent", "{}")
	if err == nil {
		t.Error("expected error for unknown tool")
	}
}

func TestTruncateOutputTail_KeepsTail(t *testing.T) {
	lines := make([]string, 10)
	for i := range lines {
		lines[i] = fmt.Sprintf("line%d", i)
	}
	in := strings.Join(lines, "\n")
	out, truncated := truncateOutputTail(in, 3, 1024)
	if !truncated {
		t.Fatal("expected truncated")
	}
	if !strings.Contains(out, "line7") || !strings.Contains(out, "line9") {
		t.Errorf("expected last 3 lines, got: %q", out)
	}
	if strings.Contains(out, "line0") {
		t.Errorf("should not contain first line: %q", out)
	}
}

func TestTruncateOutputTail_NoTruncation(t *testing.T) {
	out, truncated := truncateOutputTail("short", 100, 1024)
	if truncated {
		t.Error("should not truncate short input")
	}
	if out != "short" {
		t.Errorf("unexpected: %q", out)
	}
}

func TestReadFileLines_ByteBudget(t *testing.T) {
	// Each line is ~100 chars. With a 250 byte budget, should stop before 3 lines.
	line := strings.Repeat("x", 100)
	content := line + "\n" + line + "\n" + line + "\n" + line
	f := writeTempFile(t, content)
	lines, hasMore, err := readFileLines(f, 0, 100, 250)
	if err != nil {
		t.Fatal(err)
	}
	if !hasMore {
		t.Error("expected hasMore=true (byte budget exceeded)")
	}
	if len(lines) > 3 {
		t.Errorf("byte budget should have limited lines, got %d", len(lines))
	}
}

func TestWriteTruncationTempFile(t *testing.T) {
	path, err := writeTruncationTempFile("test output")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	if !strings.HasPrefix(path, os.TempDir()) {
		t.Errorf("expected temp dir, got: %s", path)
	}
	if !strings.Contains(path, "pigeon-bash-") {
		t.Errorf("expected pigeon-bash prefix, got: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "test output" {
		t.Errorf("unexpected content: %q", data)
	}
}
