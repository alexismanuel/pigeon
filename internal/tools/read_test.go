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

// ── readFileLines ─────────────────────────────────────────────────────────────

func TestReadFileLines_basic(t *testing.T) {
	f := writeTempFile(t, "a\nb\nc\nd\ne")
	lines, hasMore, err := readFileLines(f, 0, 100, readMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	if hasMore {
		t.Error("expected hasMore=false")
	}
	if len(lines) != 5 || lines[0] != "a" || lines[4] != "e" {
		t.Errorf("unexpected lines: %v", lines)
	}
}

func TestReadFileLines_offsetAndLimit(t *testing.T) {
	f := writeTempFile(t, "a\nb\nc\nd\ne")
	// skip 1, take 2 → ["b","c"]
	lines, hasMore, err := readFileLines(f, 1, 2, readMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	if !hasMore {
		t.Error("expected hasMore=true")
	}
	if len(lines) != 2 || lines[0] != "b" || lines[1] != "c" {
		t.Errorf("unexpected lines: %v", lines)
	}
}

func TestReadFileLines_longLineTruncated(t *testing.T) {
	long := strings.Repeat("x", readMaxLineLen+10)
	f := writeTempFile(t, long)
	lines, _, err := readFileLines(f, 0, 10, readMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	if len([]rune(lines[0])) > readMaxLineLen+5 {
		t.Errorf("line not truncated: len=%d", len(lines[0]))
	}
	if !strings.HasSuffix(lines[0], "…") {
		t.Error("expected truncation marker")
	}
}

// ── addReadLineNumbers ────────────────────────────────────────────────────────

func TestAddReadLineNumbers(t *testing.T) {
	lines := []string{"alpha", "beta", "gamma"}
	out := addReadLineNumbers(lines, 1)
	got := strings.Split(out, "\n")
	if len(got) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(got))
	}
	for i, l := range got {
		if !strings.Contains(l, "│") {
			t.Errorf("line %d missing separator: %q", i, l)
		}
		lineNum := i + 1
		if !strings.Contains(l, fmt.Sprintf("%d│", lineNum)) {
			t.Errorf("line %d missing number: %q", i, l)
		}
	}
}

func TestAddReadLineNumbers_offsetStart(t *testing.T) {
	lines := []string{"hello"}
	out := addReadLineNumbers(lines, 42)
	if !strings.Contains(out, "42│") {
		t.Errorf("expected line number 42, got: %q", out)
	}
}

// ── readSuggestSimilar ────────────────────────────────────────────────────────

func TestReadSuggestSimilar(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "foobar.go"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "foobaz.go"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "unrelated.txt"), []byte("x"), 0o644)

	suggestions := readSuggestSimilar(filepath.Join(dir, "foo.go"))
	if len(suggestions) == 0 {
		t.Error("expected at least one suggestion")
	}
	for _, s := range suggestions {
		if strings.Contains(s, "unrelated") {
			t.Errorf("unrelated file appeared in suggestions: %v", suggestions)
		}
	}
}

// ── execRead integration ──────────────────────────────────────────────────────

func TestExecRead_fileNotFound(t *testing.T) {
	e := &Executor{baseDir: t.TempDir(), maxLines: 2000, maxBytes: 50 * 1024}
	args, _ := json.Marshal(map[string]any{"path": "ghost.go"})
	_, _, err := e.Execute(context.Background(), "read", string(args))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "file not found") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestExecRead_directory(t *testing.T) {
	dir := t.TempDir()
	e := &Executor{baseDir: dir, maxLines: 2000, maxBytes: 50 * 1024}
	args, _ := json.Marshal(map[string]any{"path": "."})
	_, _, err := e.Execute(context.Background(), "read", string(args))
	if err == nil {
		t.Fatal("expected error when reading a directory")
	}
	if !strings.Contains(err.Error(), "directory") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestExecRead_fileTooLarge(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big.bin")
	// Write a file just over the 5 MB limit.
	f, _ := os.Create(big)
	f.Seek(readMaxFileSize, 0)
	f.Write([]byte{0})
	f.Close()

	e := &Executor{baseDir: dir, maxLines: 2000, maxBytes: 50 * 1024}
	args, _ := json.Marshal(map[string]any{"path": "big.bin"})
	_, _, err := e.Execute(context.Background(), "read", string(args))
	if err == nil {
		t.Fatal("expected error for oversized file")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestExecRead_hasMoreHint(t *testing.T) {
	dir := t.TempDir()
	// Write 5 lines, read with limit=3 — hasMore should be true.
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("a\nb\nc\nd\ne"), 0o644)

	e := &Executor{baseDir: dir, maxLines: 2000, maxBytes: 50 * 1024}
	args, _ := json.Marshal(map[string]any{"path": "f.txt", "limit": 3})
	result, _, err := e.Execute(context.Background(), "read", string(args))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Showing lines") {
		t.Errorf("expected truncation hint in result: %q", result)
	}
	if !strings.Contains(result, "offset=4") {
		t.Errorf("expected resume offset in truncation hint: %q", result)
	}
}

func TestExecRead_displayNotEmpty(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "hello.go"), []byte("package main\n\nfunc main() {}\n"), 0o644)

	e := &Executor{baseDir: dir, maxLines: 2000, maxBytes: 50 * 1024}
	args, _ := json.Marshal(map[string]any{"path": "hello.go"})
	_, display, err := e.Execute(context.Background(), "read", string(args))
	if err != nil {
		t.Fatal(err)
	}
	if display == "" {
		t.Error("expected non-empty display for Go file")
	}
	// Display should contain a line-number gutter separator.
	if !strings.Contains(display, "│") {
		t.Errorf("display missing gutter separator: %q", display)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "read_test_*.txt")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(content)
	f.Close()
	return f.Name()
}
