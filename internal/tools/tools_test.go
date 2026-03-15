package tools

import (
	"context"
	"encoding/json"
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
	if out != "hello\nworld" {
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
	if out != "hello\npigeon" {
		t.Fatalf("unexpected content after edit: %q", out)
	}

	absPath := filepath.Join(tmp, "a", "b.txt")
	absReadArgs, _ := json.Marshal(map[string]any{"path": absPath})
	out, _, err = e.Execute(context.Background(), "read", string(absReadArgs))
	if err != nil {
		t.Fatalf("read absolute path failed: %v", err)
	}
	if out != "hello\npigeon" {
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

func TestTruncateOutput(t *testing.T) {
	in := "1\n2\n3\n4"
	out, truncated := truncateOutput(in, 2, 100)
	if !truncated {
		t.Fatalf("expected truncated=true")
	}
	if out != "1\n2" {
		t.Fatalf("unexpected output: %q", out)
	}

	out, truncated = truncateOutput("abcdef", 100, 3)
	if !truncated || out != "abc" {
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

	rArgs, _ := json.Marshal(map[string]any{"path": "f.txt", "offset": 2, "limit": 2})
	out, _, err := e.Execute(context.Background(), "read", string(rArgs))
	if err != nil {
		t.Fatalf("read with offset: %v", err)
	}
	if !strings.Contains(out, "b") || strings.Contains(out, "a") {
		t.Errorf("offset/limit not applied: %q", out)
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
