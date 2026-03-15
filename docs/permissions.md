# Tool Permissions

Pigeon's permission system intercepts potentially sensitive tool calls — **bash**,
**write**, and **edit** — and asks for your approval before executing them.  This
gives you full visibility and control over every action the agent takes on your
machine.

## How it works

When the agent tries to run one of the protected tools, execution **pauses** and a
permission dialog appears at the bottom of the terminal:

```
╭─────────────────────────────────────────────────────────────────╮
│ 🔒 Permission Required                                          │
│ Tool   bash  ·  action  execute                                 │
│ Path   /Users/you/workspace/myproject                           │
│                                                                 │
│  $ git commit -am "fix: handle nil pointer in parser"          │
│                                                                 │
│ [y] Allow   [s] Allow for Session   [n] Deny                   │
│ esc = deny                                                      │
╰─────────────────────────────────────────────────────────────────╯
```

### Response options

| Key | Action |
|-----|--------|
| `y` / `a` / `enter` | **Allow once** — run this specific invocation |
| `s` | **Allow for Session** — remember this exact tool + action + path combination; future identical requests are auto-approved for the rest of this session |
| `n` / `d` / `esc` | **Deny** — block execution; the agent receives an error and may recover gracefully |

### Dialog content by tool

* **bash** — shows the full command being executed
* **write** — shows the first 8 lines of the content being written (green `+` lines)
* **edit** — shows up to 5 lines being removed (red `-`) and up to 5 lines being
  added (green `+`)

## Configuration

Permissions are configured in `~/.config/pigeon/settings.json` under the
`"permissions"` key.

### Skip all permission checks

For trusted, non-interactive environments (CI pipelines, local automation) you
can disable the permission system entirely:

```json
{
  "permissions": {
    "skip_requests": true
  }
}
```

> **Warning:** `skip_requests: true` auto-approves every tool call without
> prompting.  Use with care.

### Auto-approve specific tools (allowlist)

Use `"allowed_tools"` to whitelist individual tools or fine-grained
`"toolName:action"` combinations.  Auto-approved tools never trigger a dialog.

```json
{
  "permissions": {
    "allowed_tools": [
      "read",
      "bash:ls",
      "bash:cat",
      "bash:echo"
    ]
  }
}
```

| Entry | What is auto-approved |
|-------|----------------------|
| `"bash"` | All bash commands |
| `"write"` | All write operations |
| `"bash:execute"` | bash with the `execute` action (same as `"bash"`) |
| `"edit:modify"` | edit with the `modify` action (same as `"edit"`) |

### Defaults

With no `"permissions"` section the system uses these defaults:

* `skip_requests`: `false` — every bash, write, and edit call prompts
* `allowed_tools`: `[]` — nothing is auto-approved

## Permission levels (in order of precedence)

1. **skip mode** — all requests auto-approved when `skip_requests: true`
2. **allowlist** — request auto-approved if its tool/action matches an entry in
   `allowed_tools`
3. **session auto-approve** — all requests in this session auto-approved
   (set programmatically via `permission.AutoApproveSession`)
4. **session cache** — this exact tool + action + path was previously granted
   persistently (via `[s]` in the dialog)
5. **interactive prompt** — show the dialog and wait for user input

## Protected tools

| Tool | Action | Protected by default |
|------|--------|---------------------|
| bash | execute | ✅ |
| write | create | ✅ |
| edit | modify | ✅ |
| read | — | ❌ (read-only, not protected) |

`read` is intentionally unprotected because it only reads existing files and
poses minimal risk.  You can add `"read"` to `allowed_tools` if you want
explicit confirmation for file reads.
