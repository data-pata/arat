// Package tui hosts the bubbletea-based interactive pickers used by arat.
package tui

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/data-pata/arat/internal/workspace"
)

// PickWorkspace opens an interactive picker for workspaces. It renders to
// `out` (typically stderr) so the caller's stdout stays clean for the chosen
// path. Reads from /dev/tty if available (so it works inside `$( ... )`).
// Returns the chosen workspace, or nil if the user cancelled (q / Esc /
// Ctrl+C).
func PickWorkspace(ctx context.Context, items []workspace.Workspace, out io.Writer) (*workspace.Workspace, error) {
	if len(items) == 0 {
		return nil, fmt.Errorf("no workspaces to pick from")
	}

	listItems := make([]list.Item, len(items))
	for i, ws := range items {
		listItems[i] = wsItem(ws)
	}

	delegate := list.NewDefaultDelegate()
	l := list.New(listItems, delegate, 0, 0)
	l.Title = "Pick a workspace"
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(true)

	m := &pickModel{list: l}

	opts, cleanup := programOpts(ctx, out)
	defer cleanup()
	prog := tea.NewProgram(m, opts...)
	finalModel, err := prog.Run()
	if err != nil {
		return nil, fmt.Errorf("tui: %w", err)
	}
	final := finalModel.(*pickModel)
	if final.chosen == nil {
		return nil, nil
	}
	return final.chosen, nil
}

// wsItem adapts a workspace.Workspace to the list.Item interface.
type wsItem workspace.Workspace

func (i wsItem) Title() string { return i.Name }

func (i wsItem) Description() string {
	parts := make([]string, 0, 4)
	if i.Ticket != "" {
		parts = append(parts, dimStyle.Render(i.Ticket))
	}
	if len(i.Repos) > 0 {
		parts = append(parts, fmt.Sprintf("%d repo%s", len(i.Repos), pluralS(len(i.Repos))))
	}
	flags := summarizeFlags(workspace.Workspace(i))
	if flags != "" {
		parts = append(parts, flags)
	}
	return strings.Join(parts, "  ")
}

func (i wsItem) FilterValue() string {
	v := i.Name
	if i.Ticket != "" {
		v += " " + i.Ticket
	}
	return v
}

func summarizeFlags(ws workspace.Workspace) string {
	var dirty, unpushed, stashes int
	for _, r := range ws.Repos {
		if r.Dirty {
			dirty++
		}
		if r.Unpushed {
			unpushed++
		}
		stashes += r.Stashes
	}
	parts := make([]string, 0, 3)
	if dirty > 0 {
		parts = append(parts, warnStyle.Render(fmt.Sprintf("dirty:%d", dirty)))
	}
	if unpushed > 0 {
		parts = append(parts, warnStyle.Render(fmt.Sprintf("unpushed:%d", unpushed)))
	}
	if stashes > 0 {
		parts = append(parts, warnStyle.Render(fmt.Sprintf("stashes:%d", stashes)))
	}
	return strings.Join(parts, " ")
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

var (
	dimStyle  = lipgloss.NewStyle().Faint(true)
	warnStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
)

// pickModel is the bubbletea model.
type pickModel struct {
	list   list.Model
	chosen *workspace.Workspace
}

func (m *pickModel) Init() tea.Cmd { return nil }

func (m *pickModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.list.SetSize(msg.Width, msg.Height-2)

	case tea.KeyMsg:
		// Don't intercept keys while the user is typing into the filter.
		if m.list.FilterState() == list.Filtering {
			break
		}
		switch msg.String() {
		case "ctrl+c", "esc", "q":
			return m, tea.Quit
		case "enter":
			if i, ok := m.list.SelectedItem().(wsItem); ok {
				w := workspace.Workspace(i)
				m.chosen = &w
				return m, tea.Quit
			}
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m *pickModel) View() string {
	if m.chosen != nil {
		return ""
	}
	return m.list.View()
}
