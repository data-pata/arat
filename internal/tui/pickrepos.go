package tui

import (
	"context"
	"fmt"
	"io"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/data-pata/arat/internal/workspace"
)

// PickRepos opens a multi-select picker over the given repo candidates.
// Returns the names the user selected (in candidate order), or cancelled=true
// if the user pressed q/Esc/Ctrl+C. Confirming with zero items selected is
// not allowed by the picker; the user must select at least one or cancel.
func PickRepos(ctx context.Context, candidates []workspace.RepoCandidate, out io.Writer) (selected []string, cancelled bool, err error) {
	if len(candidates) == 0 {
		return nil, false, fmt.Errorf("no repo candidates")
	}
	m := newRepoModel(candidates)
	opts, cleanup := programOpts(ctx, out)
	defer cleanup()
	prog := tea.NewProgram(m, opts...)
	final, err := prog.Run()
	if err != nil {
		return nil, false, fmt.Errorf("tui: %w", err)
	}
	rm := final.(*repoModel)
	if rm.cancelled {
		return nil, true, nil
	}
	out_ := make([]string, 0, len(rm.items))
	for _, it := range rm.items {
		if it.checked {
			out_ = append(out_, it.name)
		}
	}
	return out_, false, nil
}

// repoItem is one row in the multi-select list.
type repoItem struct {
	name    string
	checked bool
}

// repoModel is the bubbletea model for PickRepos.
type repoModel struct {
	items     []repoItem
	cursor    int
	width     int
	height    int
	cancelled bool
	confirmed bool
	hint      string // transient single-line hint shown under the list
}

func newRepoModel(candidates []workspace.RepoCandidate) *repoModel {
	items := make([]repoItem, len(candidates))
	for i, c := range candidates {
		items[i] = repoItem{name: c.Name, checked: c.Selected}
	}
	return &repoModel{items: items}
}

func (m *repoModel) Init() tea.Cmd { return nil }

func (m *repoModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tea.KeyMsg:
		m.hint = ""
		switch msg.String() {
		case "ctrl+c", "esc", "q":
			m.cancelled = true
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.items)-1 {
				m.cursor++
			}
		case "home", "g":
			m.cursor = 0
		case "end", "G":
			m.cursor = len(m.items) - 1
		case " ", "x":
			m.items[m.cursor].checked = !m.items[m.cursor].checked
		case "a":
			for i := range m.items {
				m.items[i].checked = true
			}
		case "n":
			for i := range m.items {
				m.items[i].checked = false
			}
		case "enter":
			if m.countChecked() == 0 {
				m.hint = "select at least one repo (space) or press q to cancel"
				return m, nil
			}
			m.confirmed = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *repoModel) countChecked() int {
	n := 0
	for _, it := range m.items {
		if it.checked {
			n++
		}
	}
	return n
}

func (m *repoModel) View() string {
	if m.confirmed || m.cancelled {
		return ""
	}
	var b strings.Builder
	b.WriteString(repoTitleStyle.Render("Pick repos for the workspace"))
	b.WriteString("\n\n")
	for i, it := range m.items {
		cursor := "  "
		if i == m.cursor {
			cursor = "> "
		}
		check := "[ ]"
		if it.checked {
			check = "[x]"
		}
		line := cursor + check + " " + it.name
		if i == m.cursor {
			line = repoCursorStyle.Render(line)
		} else if it.checked {
			line = repoCheckedStyle.Render(line)
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(repoHelpStyle.Render("↑/↓ move • space toggle • a all • n none • enter confirm • q cancel"))
	if m.hint != "" {
		b.WriteString("\n")
		b.WriteString(repoHintStyle.Render(m.hint))
	}
	return b.String()
}

var (
	repoTitleStyle   = lipgloss.NewStyle().Bold(true)
	repoCursorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	repoCheckedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	repoHelpStyle    = lipgloss.NewStyle().Faint(true)
	repoHintStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
)
