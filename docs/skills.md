# pigeon skills

A **skill** is a Markdown file that is injected into the conversation as a
system message when invoked. Skills encode reusable knowledge, conventions, or
step-by-step workflows for a specific task.

---

## File layout

```
~/.config/pigeon/skills/<name>/SKILL.md    ← user skill
.pigeon/skills/<name>/SKILL.md             ← project-local (overrides user)
```

- The directory name becomes the skill name (case-insensitive).
- The file must be named exactly `SKILL.md`.
- Project-local skills override user-global skills with the same name.

---

## Invoking a skill

Inside pigeon, type:

```
/skill:python-dev-guidelines
```

The SKILL.md content is appended to the conversation history as a `system`
message and persisted to the session file. Subsequent turns inherit it.

Skills also appear in the `/` autocomplete list.

---

## Writing a good skill

A skill should be self-contained and immediately actionable by the model.
Structure it as:

```markdown
# <Skill name>

One-sentence summary of when to use this skill.

## When to apply
- Bullet list of task types that trigger this skill

## Rules / conventions
1. …
2. …

## Workflow
Step-by-step instructions the model should follow.

## Examples
Concrete before/after examples or code snippets.
```

**Tips**
- Keep it focused — one skill per concern. Compose by invoking multiple skills.
- Include anti-patterns to avoid, not just positive rules.
- Reference other files in the project when helpful:
  ```
  Read `.pigeon/ARCHITECTURE.md` before making changes.
  ```
- Skills can reference other skills: "First apply the `python-dev-guidelines`
  skill, then follow the steps below."

---

## Example skill — commit conventions

```markdown
# commit-conventions

Enforces Conventional Commits format for all git commits.

## When to apply
When the user asks to commit, write a commit message, or stage changes.

## Rules
- Format: `<type>(<scope>): <description>`
- Types: feat, fix, docs, chore, refactor, test, perf, ci
- Scope is optional; use it for the package/module being changed
- Description: imperative mood, ≤72 chars, no trailing period
- Breaking changes: append `!` after scope and add `BREAKING CHANGE:` footer

## Workflow
1. Run `git diff --staged` to see what's staged
2. If nothing is staged, run `git status` and `git diff`
3. Draft a commit message following the format above
4. Confirm with the user before running `git commit`

## Examples
- `feat(tui): add interactive session picker`
- `fix(session): preserve label when model changes`
- `docs: add extension API reference`
```

---

## Example skill — Go testing conventions

```markdown
# go-testing

Conventions for writing Go tests in this project.

## When to apply
When writing or reviewing any *_test.go file.

## Rules
1. Use table-driven tests for multiple cases
2. Test files use `package foo_test` (black-box) unless testing unexported symbols
3. Use `t.TempDir()` for temporary files — never `/tmp` directly
4. Prefer `t.Fatal` for setup failures, `t.Error` for assertion failures
5. No `time.Sleep` in tests — use channels or `t.Helper` retry loops
6. Always run `go test -race ./...` before declaring tests done

## Anti-patterns
- Do not use `testify` — stdlib only
- Do not mock interfaces with `reflect`; write explicit dummy structs
```
