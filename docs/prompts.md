# pigeon prompt templates

A **prompt template** is a Markdown file whose content is inserted verbatim
into the chat input field when the user types its slash command. It is a
shorthand for long or repetitive prompts — not injected as a system message
(unlike skills).

---

## File layout

```
~/.config/pigeon/prompts/<name>.md         ← user template
.pigeon/prompts/<name>.md                  ← project-local (overrides user)
```

- The filename stem (without `.md`) becomes the command name.
- Project-local files override user-global ones with the same name.

---

## Invoking a template

Type `/promptname` (no colon, unlike skills) in the chat input. The template
content is pasted into the input field, ready to be edited before sending.

Templates appear in the `/` autocomplete list.

---

## When to use templates vs. skills

| | Prompt template | Skill |
|---|---|---|
| **Inserted as** | User message (editable) | System message (immediate) |
| **Persisted?** | No — only the final typed message is sent | Yes — added to session history |
| **Best for** | Repetitive queries, boilerplate starters | Reusable instructions, conventions |

---

## Writing a good template

Templates are inserted into the input field verbatim, so write them as if you
were typing them yourself. They can be:

- A question with placeholders:
  ```
  Explain how {{TOPIC}} works in the context of this codebase.
  ```
- A structured task:
  ```
  Review the last change I made:
  1. Identify any bugs
  2. Suggest tests
  3. Point out style issues
  ```
- A long boilerplate you type often:
  ```
  Write a Go test for the function I just edited. Use table-driven style and
  t.TempDir() for any temp files. Do not use testify.
  ```

---

## Example templates

### `review.md`

```markdown
Review my last change:
1. Are there any bugs?
2. Are edge cases handled?
3. Is there anything missing from the tests?
4. Any style or naming issues?
```

### `explain.md`

```markdown
Explain what this code does, why it exists, and what would break if it were removed.
```

### `commit.md`

```markdown
Write a Conventional Commits message for my staged changes. Run `git diff --staged` first.
```

### `pr.md`

```markdown
Write a pull request description for my current branch. Include:
- Summary (1-2 sentences)
- What changed and why
- How to test it
- Any risks or caveats
```
