// Package resources discovers and loads skills, prompt templates, and
// extensions from the user config dir and from any project-local override dir.
//
// Directory layout:
//
//	~/.config/pigeon/skills/<name>/SKILL.md    — user skill
//	~/.config/pigeon/prompts/<name>.md         — user prompt template
//	~/.config/pigeon/extensions/<name>.lua     — user Lua extension
//
//	.pigeon/skills/<name>/SKILL.md             — project-local (overrides user)
//	.pigeon/prompts/<name>.md
//	.pigeon/extensions/<name>.lua
package resources

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Skill is a loaded SKILL.md file.
type Skill struct {
	Name    string
	Content string
	Path    string
}

// Prompt is a loaded prompt-template markdown file.
type Prompt struct {
	Name    string
	Content string
	Path    string
}

// ExtensionPath holds the path to a discovered Lua extension (content loaded by M5).
type ExtensionPath struct {
	Name string
	Path string
}

// Registry holds everything loaded from disk.
type Registry struct {
	skills     map[string]Skill
	prompts    map[string]Prompt
	extensions map[string]ExtensionPath
}

// Load discovers resources from the global config dir and the project-local
// override dir. Project-local entries take precedence over global ones.
func Load() (*Registry, error) {
	r := &Registry{
		skills:     make(map[string]Skill),
		prompts:    make(map[string]Prompt),
		extensions: make(map[string]ExtensionPath),
	}

	// 1. global: ~/.config/pigeon/
	if globalDir, err := GlobalConfigDir(); err == nil {
		r.loadSkills(filepath.Join(globalDir, "skills"))
		r.loadPrompts(filepath.Join(globalDir, "prompts"))
		r.loadExtensionPaths(filepath.Join(globalDir, "extensions"))
	}

	// 2. project-local: .pigeon/  (cwd-relative, overrides global)
	r.loadSkills(filepath.Join(".pigeon", "skills"))
	r.loadPrompts(filepath.Join(".pigeon", "prompts"))
	r.loadExtensionPaths(filepath.Join(".pigeon", "extensions"))

	return r, nil
}

// LoadFrom is like Load but uses explicit base directories instead of
// the defaults — useful for tests.
func LoadFrom(globalDir, localDir string) (*Registry, error) {
	r := &Registry{
		skills:     make(map[string]Skill),
		prompts:    make(map[string]Prompt),
		extensions: make(map[string]ExtensionPath),
	}
	if globalDir != "" {
		r.loadSkills(filepath.Join(globalDir, "skills"))
		r.loadPrompts(filepath.Join(globalDir, "prompts"))
		r.loadExtensionPaths(filepath.Join(globalDir, "extensions"))
	}
	if localDir != "" {
		r.loadSkills(filepath.Join(localDir, "skills"))
		r.loadPrompts(filepath.Join(localDir, "prompts"))
		r.loadExtensionPaths(filepath.Join(localDir, "extensions"))
	}
	return r, nil
}

// GlobalConfigDir returns ~/.config/pigeon.
func GlobalConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "pigeon"), nil
}

// GetSkill returns the skill with the given name (case-insensitive).
func (r *Registry) GetSkill(name string) (Skill, bool) {
	s, ok := r.skills[strings.ToLower(name)]
	return s, ok
}

// GetPrompt returns the prompt template with the given name (case-insensitive).
func (r *Registry) GetPrompt(name string) (Prompt, bool) {
	p, ok := r.prompts[strings.ToLower(name)]
	return p, ok
}

// GetExtensionPath returns the Lua extension path for the given name.
func (r *Registry) GetExtensionPath(name string) (ExtensionPath, bool) {
	e, ok := r.extensions[strings.ToLower(name)]
	return e, ok
}

// ListSkills returns all loaded skills sorted by name.
func (r *Registry) ListSkills() []Skill {
	out := make([]Skill, 0, len(r.skills))
	for _, s := range r.skills {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ListPrompts returns all loaded prompt templates sorted by name.
func (r *Registry) ListPrompts() []Prompt {
	out := make([]Prompt, 0, len(r.prompts))
	for _, p := range r.prompts {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ListExtensionPaths returns all discovered extension paths sorted by name.
func (r *Registry) ListExtensionPaths() []ExtensionPath {
	out := make([]ExtensionPath, 0, len(r.extensions))
	for _, e := range r.extensions {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ── internal loaders ──────────────────────────────────────────────────────────

func (r *Registry) loadSkills(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return // dir absent — not an error
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillFile := filepath.Join(dir, entry.Name(), "SKILL.md")
		content, err := os.ReadFile(skillFile)
		if err != nil {
			continue
		}
		name := strings.ToLower(entry.Name())
		r.skills[name] = Skill{
			Name:    name,
			Content: string(content),
			Path:    skillFile,
		}
	}
}

func (r *Registry) loadPrompts(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".md")
		promptFile := filepath.Join(dir, entry.Name())
		content, err := os.ReadFile(promptFile)
		if err != nil {
			continue
		}
		key := strings.ToLower(name)
		r.prompts[key] = Prompt{
			Name:    key,
			Content: string(content),
			Path:    promptFile,
		}
	}
}

func (r *Registry) loadExtensionPaths(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".lua") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".lua")
		key := strings.ToLower(name)
		r.extensions[key] = ExtensionPath{
			Name: key,
			Path: filepath.Join(dir, entry.Name()),
		}
	}
}
