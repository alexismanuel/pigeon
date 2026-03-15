package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"pigeon/internal/provider/openrouter"
	"pigeon/internal/session"
)

// ── messages ──────────────────────────────────────────────────────────────────

type nodePickedMsg struct{ nodeID string }
type nodePickCanceledMsg struct{}

// ── nodeRow ───────────────────────────────────────────────────────────────────

type nodeRow struct {
	nodeID      string
	treePrefix  string
	displayRole string // pre-computed label: "user", "asst", "call", "tool", " sys"
	preview     string
	isCurrent   bool
	hasChildren bool // node has children in the current filtered view
	folded      bool // node is collapsed (children hidden)
}

// ── nodePicker ────────────────────────────────────────────────────────────────

type nodePicker struct {
	// raw data for rebuilding on filter toggle
	nodes         []session.Node
	currentNodeID string

	rows           []nodeRow
	cursor         int
	offset         int
	hideTools      bool
	collapsedNodes map[string]bool
	width          int
	height         int
}

func newNodePicker(nodes []session.Node, currentNodeID string, width, height int) nodePicker {
	p := nodePicker{
		nodes:          nodes,
		currentNodeID:  currentNodeID,
		hideTools:      false,
		collapsedNodes: make(map[string]bool),
		width:          width,
		height:         height,
	}
	p.rows = buildNodeRows(nodes, currentNodeID, false, p.collapsedNodes)
	p.cursor, p.offset = nodePickerCursor(p.rows, p.listHeightFor(height))
	return p
}

func (p nodePicker) listHeight() int {
	return p.listHeightFor(p.height)
}

func (p nodePicker) listHeightFor(h int) int {
	// 1 header + 1 separator + 1 footer = 3 overhead
	v := h - 3
	if v < 3 {
		return 3
	}
	return v
}

// nodePickerCursor finds the index of the current node and computes the
// initial scroll offset so it is visible.
func nodePickerCursor(rows []nodeRow, vis int) (cursor, offset int) {
	for i, r := range rows {
		if r.isCurrent {
			cursor = i
			break
		}
	}
	if cursor >= vis {
		offset = cursor - vis + 1
	}
	return
}

func (p nodePicker) Update(msg tea.Msg) (nodePicker, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.width = msg.Width
		p.height = msg.Height
		return p, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "ctrl+c":
			return p, func() tea.Msg { return nodePickCanceledMsg{} }

		case "enter":
			if len(p.rows) > 0 {
				id := p.rows[p.cursor].nodeID
				return p, func() tea.Msg { return nodePickedMsg{nodeID: id} }
			}
			return p, nil

		case "up", "ctrl+p":
			if p.cursor > 0 {
				p.cursor--
				if p.cursor < p.offset {
					p.offset = p.cursor
				}
			}
			return p, nil

		case "down", "ctrl+n":
			if p.cursor < len(p.rows)-1 {
				p.cursor++
				vis := p.listHeight()
				if p.cursor >= p.offset+vis {
					p.offset = p.cursor - vis + 1
				}
			}
			return p, nil

		case "left":
			if p.cursor < len(p.rows) {
				row := p.rows[p.cursor]
				if row.hasChildren && !row.folded {
					p.collapsedNodes[row.nodeID] = true
					p.rows = buildNodeRows(p.nodes, p.currentNodeID, p.hideTools, p.collapsedNodes)
					// cursor stays on the same node; clamp in case list shrank
					if p.cursor >= len(p.rows) {
						p.cursor = max(0, len(p.rows)-1)
					}
				}
			}
			return p, nil

		case "right":
			if p.cursor < len(p.rows) {
				row := p.rows[p.cursor]
				if row.folded {
					delete(p.collapsedNodes, row.nodeID)
					p.rows = buildNodeRows(p.nodes, p.currentNodeID, p.hideTools, p.collapsedNodes)
				}
			}
			return p, nil

		case "t":
			p.hideTools = !p.hideTools
			p.rows = buildNodeRows(p.nodes, p.currentNodeID, p.hideTools, p.collapsedNodes)
			p.cursor, p.offset = nodePickerCursor(p.rows, p.listHeight())
			return p, nil
		}
	}
	return p, nil
}

func (p nodePicker) View() string {
	var b strings.Builder

	// header
	filterNote := ""
	if p.hideTools {
		filterNote = pickerDimStyle.Render("  [tools hidden — press t to show]")
	}
	header := pickerHintStyle.Render("  Navigate conversation tree")
	if filterNote != "" {
		header = header + "  " + filterNote
	}
	b.WriteString(header + "\n")

	// separator
	sep := strings.Repeat("─", max(0, p.width-4))
	b.WriteString(pickerDimStyle.Render("  "+sep) + "\n")

	// list
	vis := p.listHeight()
	end := p.offset + vis
	if end > len(p.rows) {
		end = len(p.rows)
	}

	if len(p.rows) == 0 {
		b.WriteString(pickerDimStyle.Render("  (empty tree)\n"))
	} else {
		for i := p.offset; i < end; i++ {
			row := p.rows[i]
			selected := i == p.cursor

			currentMark := " "
			if row.isCurrent {
				currentMark = nodeCurrentStyle.Render("●")
			}

			roleStr := nodeRoleStyle(row.displayRole)

			foldIndicator := ""
			foldIndicatorLen := 0
			if row.folded {
				foldIndicator = pickerDimStyle.Render(" […]")
				foldIndicatorLen = 4
			}

			overhead := 2 + len(row.treePrefix) + 2 + 4 + 1 + foldIndicatorLen // "▶ " + prefix + mark + role + space + fold
			previewMax := p.width - overhead
			if previewMax < 10 {
				previewMax = 10
			}
			preview := truncStr(row.preview, previewMax)

			line := fmt.Sprintf("%s%s %s %s", row.treePrefix, currentMark, roleStr, preview)

			if selected {
				b.WriteString(pickerCursorStyle.Render("▶ ") + pickerSelectedStyle.Render(line) + foldIndicator + "\n")
			} else {
				b.WriteString("  " + line + foldIndicator + "\n")
			}
		}
	}

	// footer
	toolHint := "t hide tools"
	if p.hideTools {
		toolHint = "t show tools"
	}
	b.WriteString(pickerHintStyle.Render(fmt.Sprintf(
		"  ↑↓ navigate • ←/→ fold/expand • enter checkout • %s • esc cancel   %d nodes",
		toolHint, len(p.rows),
	)))

	return b.String()
}

// ── tree building ─────────────────────────────────────────────────────────────

func isToolNode(n session.Node) bool {
	if n.Message.Role == "tool" {
		return true
	}
	// Assistant messages that only dispatch tool calls (no text for the user).
	if n.Message.Role == "assistant" &&
		len(n.Message.ToolCalls) > 0 &&
		strings.TrimSpace(n.Message.Content) == "" {
		return true
	}
	return false
}

func buildNodeRows(nodes []session.Node, currentNodeID string, hideTools bool, collapsedNodes map[string]bool) []nodeRow {
	nodeByID := make(map[string]session.Node, len(nodes))
	for _, n := range nodes {
		nodeByID[n.ID] = n
	}

	// Build the children map — with or without tool-node filtering.
	children := make(map[string][]session.Node)

	if hideTools {
		// Resolve each visible node's effective parent by walking up the
		// parent chain until we reach a non-tool node (or the root).
		effectiveParent := func(n session.Node) string {
			parentID := strings.TrimSpace(n.ParentID)
			for parentID != "" {
				parent, ok := nodeByID[parentID]
				if !ok {
					break
				}
				if !isToolNode(parent) {
					return parentID
				}
				parentID = strings.TrimSpace(parent.ParentID)
			}
			return parentID
		}

		for _, n := range nodes {
			if isToolNode(n) {
				continue
			}
			effParent := effectiveParent(n)
			children[effParent] = append(children[effParent], n)
		}
	} else {
		for _, n := range nodes {
			parent := strings.TrimSpace(n.ParentID)
			children[parent] = append(children[parent], n)
		}
	}

	for parent := range children {
		sortByRecorded(children[parent])
	}

	var rows []nodeRow
	var walk func(parentID, indent string)
	walk = func(parentID, indent string) {
		kids := children[parentID]
		numKids := len(kids)
		for i, kid := range kids {
			isLast := i == numKids-1

			var prefix, nextIndent string
			if numKids == 1 {
				// Straight continuation — no branch connector.
				prefix = indent
				nextIndent = indent
			} else if isLast {
				// Last branch: "└─ " (3 chars). Children indented 5 chars.
				prefix = indent + "└─ "
				nextIndent = indent + "     "
			} else {
				// Non-last branch: "├─ " (3 chars). Children "│    " (5 chars).
				prefix = indent + "├─ "
				nextIndent = indent + "│    "
			}

			displayRole, preview := nodeDisplayInfo(kid.Message)
			kidHasChildren := len(children[kid.ID]) > 0
			kidFolded := kidHasChildren && collapsedNodes[kid.ID]

			rows = append(rows, nodeRow{
				nodeID:      kid.ID,
				treePrefix:  prefix,
				displayRole: displayRole,
				preview:     preview,
				isCurrent:   kid.ID == currentNodeID,
				hasChildren: kidHasChildren,
				folded:      kidFolded,
			})
			if !kidFolded {
				walk(kid.ID, nextIndent)
			}
		}
	}
	walk("", "")
	return rows
}

// nodeDisplayInfo returns the display role label and message preview for a node.
func nodeDisplayInfo(msg openrouter.Message) (role, preview string) {
	switch msg.Role {
	case "user":
		return "user", nodePreviewText(msg.Content)

	case "assistant":
		if len(msg.ToolCalls) > 0 && strings.TrimSpace(msg.Content) == "" {
			// Pure tool-dispatch message — show the called tool names.
			names := make([]string, 0, len(msg.ToolCalls))
			for _, tc := range msg.ToolCalls {
				names = append(names, tc.Function.Name)
			}
			return "call", strings.Join(names, ", ")
		}
		return "asst", nodePreviewText(msg.Content)

	case "tool":
		name := msg.Name
		if name == "" {
			name = "?"
		}
		content := nodePreviewText(msg.Content)
		if content != "" {
			return "tool", name + ": " + content
		}
		return "tool", name

	case "system":
		return " sys", nodePreviewText(msg.Content)

	case "cmd":
		return " cmd", msg.Content

	default:
		return msg.Role, nodePreviewText(msg.Content)
	}
}

func nodePreviewText(s string) string {
	p := summarize(s)
	if p == "(no output)" {
		return ""
	}
	return p
}

// ── styles ────────────────────────────────────────────────────────────────────

var nodeCurrentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true)

func nodeRoleStyle(role string) string {
	switch role {
	case "user":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Render("user")
	case "asst":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Render("asst")
	case "call":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("13")).Render("call")
	case "tool":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Render("tool")
	case " sys":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render(" sys")
	case " cmd":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Render(" cmd")
	default:
		return pickerDimStyle.Render(role)
	}
}
