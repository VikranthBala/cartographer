package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/vikranthBala/cartographer/internal/graph"
)

// ── Styles ────────────────────────────────────────────────────────────────────

var (
	styleTitle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("205")).
			MarginBottom(1)

	styleCategory = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("39")).
			MarginTop(1)

	styleHeader = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240"))

	styleAdded  = lipgloss.NewStyle().Foreground(lipgloss.Color("82"))
	styleNormal = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))

	styleSelected = lipgloss.NewStyle().
			Background(lipgloss.Color("57")).
			Foreground(lipgloss.Color("255"))

	styleHelp = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			MarginTop(1)

	styleStateEst    = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	styleStateListen = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	styleStateWait   = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
)

type col struct {
	title string
	width int
}

var columns = []col{
	{"PROCESS", 18},
	{"SERVICE", 16},
	{"HOST", 26},
	{"REMOTE", 21},
	{"PROTO", 6},
	{"STATE", 12},
	{"AGE", 5},
}

// ── Messages ──────────────────────────────────────────────────────────────────

type deltaMsg struct{ delta graph.Delta }

func waitForDelta(ch <-chan graph.Delta) tea.Cmd {
	return func() tea.Msg {
		d, ok := <-ch
		if !ok {
			return nil
		}
		return deltaMsg{delta: d}
	}
}

// ── Model ─────────────────────────────────────────────────────────────────────

type nodeState struct {
	node      graph.Node
	highlight bool
}

// rowItem allows the cursor to select either a category header or a connection node
type rowItem struct {
	isHeader bool
	category string // Populated if it's a header
	nodeID   string // Populated if it's a node
}

type Model struct {
	nodes  map[string]*nodeState
	deltas <-chan graph.Delta

	// Layout & State
	rowItems   []rowItem
	lines      []string
	cursor     int
	cursorLine int // Visual line index of the cursor
	viewportY  int
	width      int
	height     int
	collapsed  map[string]bool

	// Inputs
	quitting   bool
	textInput  textinput.Model
	filtering  bool
	filterText string
}

func New(store *graph.Store) Model {
	ti := textinput.New()
	ti.Placeholder = "Search process, service, or host..."
	ti.CharLimit = 50
	ti.Width = 40

	m := Model{
		nodes:     make(map[string]*nodeState),
		deltas:    store.Deltas(),
		textInput: ti,
		collapsed: make(map[string]bool),
		height:    24, // Fallback height before WindowSizeMsg arrives
	}
	for _, n := range store.Snapshot() {
		n := n
		m.nodes[n.ID] = &nodeState{node: n}
	}
	return m
}

func (m Model) Init() tea.Cmd {
	return waitForDelta(m.deltas)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.rebuild()

	case tea.KeyMsg:
		if m.filtering {
			switch msg.String() {
			case "esc", "enter":
				m.filtering = false
				m.textInput.Blur()
				m.filterText = m.textInput.Value()
				m.cursor = 0
				m.rebuild()
				return m, nil
			default:
				var cmd tea.Cmd
				m.textInput, cmd = m.textInput.Update(msg)
				m.filterText = m.textInput.Value()
				m.cursor = 0
				m.rebuild()
				return m, cmd
			}
		}

		switch msg.String() {
		case "q", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
				m.rebuild()
			}
		case "down", "j":
			if m.cursor < len(m.rowItems)-1 {
				m.cursor++
				m.rebuild()
			}
		case "/":
			m.filtering = true
			m.textInput.Focus()
			return m, nil
		case "enter", " ":
			// Toggle collapse based on the selected item (Header or Node)
			if len(m.rowItems) > 0 && m.cursor >= 0 && m.cursor < len(m.rowItems) {
				item := m.rowItems[m.cursor]
				cat := item.category

				// If they pressed enter on a node, collapse its parent category
				if !item.isHeader {
					if ns, ok := m.nodes[item.nodeID]; ok {
						cat = ns.node.Category
						if cat == "" {
							cat = "unknown"
						}
					}
				}

				m.collapsed[cat] = !m.collapsed[cat]
				m.rebuild()
			}
		}

	case deltaMsg:
		m.applyDelta(msg.delta)
		m.rebuild()
		cmds = append(cmds, waitForDelta(m.deltas))
	}

	return m, tea.Batch(cmds...)
}

func (m *Model) applyDelta(d graph.Delta) {
	for _, change := range d.Changes {
		id := change.Node.ID
		switch change.Type {
		case graph.NodeAdded:
			m.nodes[id] = &nodeState{node: change.Node, highlight: true}
		case graph.NodeUpdated:
			if ns, ok := m.nodes[id]; ok {
				ns.node = change.Node
				ns.highlight = true
			}
		case graph.NodeRemoved:
			delete(m.nodes, id)
		}
	}
}

// rebuild reconstructs the visual lines and syncs the viewport/cursor
func (m *Model) rebuild() {
	m.rowItems = nil
	m.lines = nil
	m.cursorLine = 0

	groups, categories := m.groupByCategory()

	for _, cat := range categories {
		nodes := groups[cat]
		if len(nodes) == 0 {
			continue
		}

		// Add Header as a selectable item
		m.rowItems = append(m.rowItems, rowItem{isHeader: true, category: cat})
		isHeaderSelected := !m.filtering && len(m.rowItems)-1 == m.cursor
		if isHeaderSelected {
			m.cursorLine = len(m.lines)
		}

		indicator := "▾"
		if m.collapsed[cat] {
			indicator = "▸"
		}

		headerText := fmt.Sprintf("%s %s (%d)", indicator, strings.ToUpper(cat), len(nodes))

		// If the header itself is selected, highlight it
		if isHeaderSelected {
			m.lines = append(m.lines, styleSelected.Render(headerText))
		} else {
			m.lines = append(m.lines, styleCategory.Render(headerText))
		}

		if m.collapsed[cat] {
			m.lines = append(m.lines, "") // Visual spacing
			continue
		}

		m.lines = append(m.lines, styleHeader.Render(renderHeader()))

		for _, ns := range nodes {
			// Add Node as a selectable item
			m.rowItems = append(m.rowItems, rowItem{isHeader: false, nodeID: ns.node.ID})

			// Is this the selected row?
			isSelected := !m.filtering && len(m.rowItems)-1 == m.cursor
			if isSelected {
				m.cursorLine = len(m.lines)
			}

			m.lines = append(m.lines, renderRow(ns, isSelected))
			ns.highlight = false // clear transient highlight after capturing layout
		}
		m.lines = append(m.lines, "")
	}

	// Clamp cursor if items were removed or collapsed
	if m.cursor >= len(m.rowItems) {
		m.cursor = len(m.rowItems) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}

	m.syncViewport()
}

func (m *Model) syncViewport() {
	headerLines := 3 // title + empty lines
	if m.filtering || m.filterText != "" {
		headerLines += 2
	}
	footerLines := 2

	availableHeight := m.height - headerLines - footerLines
	if availableHeight < 5 {
		availableHeight = 5 // sane minimum
	}

	// Adjust viewport to keep cursor visible
	if m.cursorLine < m.viewportY {
		m.viewportY = m.cursorLine
	} else if m.cursorLine >= m.viewportY+availableHeight {
		m.viewportY = m.cursorLine - availableHeight + 1
	}
}

// ── View ──────────────────────────────────────────────────────────────────────

func (m Model) View() string {
	if m.quitting {
		return "bye.\n"
	}

	var b strings.Builder
	b.WriteString(styleTitle.Render("● cartographer") + "\n")

	if m.filtering || m.filterText != "" {
		b.WriteString(m.textInput.View() + "\n\n")
	} else {
		b.WriteString("\n")
	}

	if len(m.nodes) == 0 {
		b.WriteString(styleNormal.Render("  waiting for connections…") + "\n")
	} else if len(m.rowItems) == 0 {
		b.WriteString(styleNormal.Render("  no matching connections found.") + "\n")
	} else {
		// Calculate what slice of lines we can show
		headerLines := 3
		if m.filtering || m.filterText != "" {
			headerLines += 2
		}
		footerLines := 2
		availableHeight := m.height - headerLines - footerLines
		if availableHeight < 5 {
			availableHeight = len(m.lines) // fallback if sizes are weird
		}

		start := m.viewportY
		end := start + availableHeight
		if end > len(m.lines) {
			end = len(m.lines)
		}
		if start > end {
			start = end
		}

		if start >= 0 && end <= len(m.lines) {
			b.WriteString(strings.Join(m.lines[start:end], "\n") + "\n")
		}
	}

	helpText := "↑/↓ navigate   / search   enter toggle collapse   q quit"
	if m.filtering {
		helpText = "esc/enter finish search"
	}

	// Draw footer at the bottom
	b.WriteString("\n" + styleHelp.Render(helpText))

	return b.String()
}

func renderHeader() string {
	var parts []string
	for _, c := range columns {
		parts = append(parts, padRight(c.title, c.width))
	}
	return "  " + strings.Join(parts, " ")
}

func renderRow(ns *nodeState, selected bool) string {
	n := ns.node
	remote := fmt.Sprintf("%s:%d", n.RemoteAddr, n.RemotePort)
	procInfo := fmt.Sprintf("%s (%d)", n.ProcessName, n.PID)

	cells := []string{
		padRight(procInfo, columns[0].width),
		padRight(n.ServiceLabel, columns[1].width),
		padRight(n.RemoteHost, columns[2].width),
		padRight(remote, columns[3].width),
		padRight(n.Protocol, columns[4].width),
		renderState(padRight(n.State, columns[5].width)),
		padRight(fmtAge(n.FirstSeen), columns[6].width),
	}

	line := "  " + strings.Join(cells, " ")

	switch {
	case selected:
		return styleSelected.Render(line)
	case ns.highlight:
		return styleAdded.Render(line)
	default:
		return line
	}
}

func renderState(state string) string {
	trimmed := strings.TrimSpace(state)
	switch trimmed {
	case "ESTABLISHED":
		return styleStateEst.Render(state)
	case "LISTEN":
		return styleStateListen.Render(state)
	default:
		if strings.Contains(trimmed, "WAIT") {
			return styleStateWait.Render(state)
		}
		return styleNormal.Render(state)
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func (m Model) groupByCategory() (map[string][]*nodeState, []string) {
	groups := make(map[string][]*nodeState)
	query := strings.ToLower(m.filterText)

	for _, ns := range m.nodes {
		if query != "" {
			match := strings.Contains(strings.ToLower(ns.node.ProcessName), query) ||
				strings.Contains(strings.ToLower(ns.node.ServiceLabel), query) ||
				strings.Contains(strings.ToLower(ns.node.RemoteHost), query) ||
				strings.Contains(fmt.Sprintf("%d", ns.node.PID), query) ||
				strings.Contains(strings.ToLower(ns.node.Category), query)

			if !match {
				continue
			}
		}

		cat := ns.node.Category
		if cat == "" {
			cat = "unknown"
		}
		groups[cat] = append(groups[cat], ns)
	}

	for _, g := range groups {
		sort.Slice(g, func(i, j int) bool {
			return g[i].node.ServiceLabel < g[j].node.ServiceLabel
		})
	}

	cats := make([]string, 0, len(groups))
	for cat := range groups {
		cats = append(cats, cat)
	}
	sort.Strings(cats)

	return groups, cats
}

func padRight(s string, width int) string {
	if len(s) >= width {
		return s[:width]
	}
	return s + strings.Repeat(" ", width-len(s))
}

func fmtAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
}

// ── Entry point ───────────────────────────────────────────────────────────────

func Run(store *graph.Store) error {
	p := tea.NewProgram(New(store), tea.WithAltScreen())
	_, err := p.Run()
	return err
}
