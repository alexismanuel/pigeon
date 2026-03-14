# pigeon tools

pigeon exposes four built-in tools to the model. Every tool call is executed on
the local machine in the current working directory.

---

## read

Read a UTF-8 text file, optionally sliced by line range.

```json
{ "path": "src/main.go", "offset": 40, "limit": 60 }
```

| Field    | Type    | Required | Description                                |
|----------|---------|----------|--------------------------------------------|
| `path`   | string  | ✓        | Absolute or cwd-relative path              |
| `offset` | integer |          | First line to return (1-indexed)           |
| `limit`  | integer |          | Maximum number of lines to return          |

**Truncation** — output is capped at 2000 lines or 50 KB, whichever is hit
first. When truncated, `[output truncated]` is appended. Use `offset`/`limit`
to page through large files.

**Usage guidance**
- Read a file before editing it. `edit` requires an exact match of `oldText`;
  reading first ensures you have the current content.
- Prefer sliced reads when you only need a section (saves context).
- Binary files and non-UTF-8 files return an error; use `bash` with
  `xxd`/`file` for those.

---

## write

Write content to a file. Creates the file and all parent directories if they
do not exist. Overwrites without prompting.

```json
{ "path": "internal/foo/bar.go", "content": "package foo\n" }
```

| Field     | Type   | Required | Description             |
|-----------|--------|----------|-------------------------|
| `path`    | string | ✓        | Destination path        |
| `content` | string | ✓        | Full file content       |

**Usage guidance**
- Use for new files or complete rewrites.
- For partial changes use `edit` instead — it preserves surrounding context.
- Always provide the complete intended file content; the previous content is
  entirely discarded.

---

## edit

Replace one exact occurrence of `oldText` with `newText` inside a file.
Fails if `oldText` matches zero times (not found) or more than once (ambiguous).

```json
{
  "path": "internal/tui/model.go",
  "oldText": "func (m Model) View() string {",
  "newText": "func (m Model) View() string { // updated"
}
```

| Field     | Type   | Required | Description                        |
|-----------|--------|----------|------------------------------------|
| `path`    | string | ✓        | File to edit                       |
| `oldText` | string | ✓        | Exact text to find (unique in file)|
| `newText` | string | ✓        | Replacement text                   |

**Usage guidance**
- `oldText` must match the file **exactly** — including whitespace, indentation,
  and newlines. Read the file first to get the literal text.
- Include enough surrounding lines to make the match unique. A bare function
  signature may appear multiple times; adding an adjacent line or two prevents
  ambiguity.
- For multiple edits to the same file, apply them sequentially (each edit
  reads the result of the previous one).

---

## bash

Execute an arbitrary shell command via `bash -lc`. Combines stdout and stderr.
Runs in the current working directory.

```json
{ "command": "go test ./... -timeout=60s", "timeout": 90 }
```

| Field     | Type    | Required | Description                          |
|-----------|---------|----------|--------------------------------------|
| `command` | string  | ✓        | Shell command                        |
| `timeout` | integer |          | Seconds before SIGKILL (default: none)|

**Truncation** — same 2000-line / 50 KB cap as `read`.

**Usage guidance**
- Prefer `bash` for exploration (`ls`, `find`, `grep`, `rg`, `go build`).
- Always run `go build ./...` and `go test ./...` after changing Go code.
- Use `timeout` for commands that may hang (e.g., network calls, long builds).
- The shell is `bash -lc`, so login profile is sourced; environment variables
  set in the user's shell (e.g., `OPENROUTER_API_KEY`) are available.
- Avoid interactive commands (`vim`, `less`); they will hang.
