package tui

import "strings"

type commandDef struct {
	name string
	args string
	desc string
}

// builtinCommands are always present regardless of loaded resources.
var builtinCommands = []commandDef{
	{"/model", "[id]", "switch model — interactive picker if no id given"},
	{"/new", "", "start a new session"},
	{"/sessions", "", "browse and resume a previous session"},
	{"/label", "[text]", "label the current session (no arg = show current)"},
	{"/system", "[text]", "set system prompt for this session (no arg = show current)"},
	{"/tree", "", "show conversation tree"},
	{"/quit", "", "exit pigeon"},
}

// filterCommands returns commands (builtins + extras) whose name has the given
// prefix. A query of "/" alone returns everything.
func filterCommands(query string, extra []commandDef) []commandDef {
	q := strings.ToLower(strings.TrimSpace(query))
	all := append(builtinCommands, extra...)
	if q == "/" {
		return all
	}
	var out []commandDef
	for _, cmd := range all {
		if strings.HasPrefix(cmd.name, q) {
			out = append(out, cmd)
		}
	}
	return out
}
