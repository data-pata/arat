package tui

import (
	"context"
	"fmt"
	"io"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/data-pata/arat/internal/workspace"
)

// PickRepos opens a multi-select picker over the given repo candidates.
// Returns the names the user selected (in candidate order), or cancelled=true
// if the user pressed q/Esc/Ctrl+C. Confirming with zero items selected is
// not allowed by the picker; the user must select at least one or cancel.
//
// Built on bubbles/list like the issue picker, for the same look and the two
// things a 60-clone root needs: pagination (a plain full-height render gets
// its top clipped by the terminal, hiding exactly the pre-checked defaults
// that sort first) and "/" filtering. Checkbox state lives behind a pointer
// shared by the filtered and unfiltered views, so toggling while filtered
// mutates the one true state and the pinned selected-counter never lies.
type repoState struct {
	name    string
	source  string
	checked bool
}

type repoItem struct{ s *repoState }

func (i repoItem) Title() string {
	check := "[ ]"
	if i.s.checked {
		check = "[x]"
	}
	return check + " " + i.s.name
}

func (i repoItem) Description() string {
	if i.s.checked {
		return dimStyle.Render(i.s.source + " · selected")
	}
	return dimStyle.Render(i.s.source)
}
func (i repoItem) FilterValue() string { return i.s.name + " " + i.s.source }

type repoModel struct {
	list      list.Model
	states    []*repoState // candidate order, the source of truth
	cancelled bool
	confirmed bool
}

func newRepoModel(candidates []workspace.RepoCandidate) *repoModel {
	states := make([]*repoState, len(candidates))
	items := make([]list.Item, len(candidates))
	for i, c := range candidates {
		source := c.Source
		if source == "" {
			source = "clone"
		}
		states[i] = &repoState{name: c.Name, source: source, checked: c.Selected}
		items[i] = repoItem{s: states[i]}
	}

	delegate := list.NewDefaultDelegate()
	l := list.New(items, delegate, 0, 0)
	l.SetShowStatusBar(true)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(true)
	l.AdditionalShortHelpKeys = func() []key.Binding {
		return []key.Binding{
			key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "toggle")),
			key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "all")),
			key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "none")),
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "confirm")),
		}
	}

	m := &repoModel{list: l, states: states}
	m.refreshTitle()
	return m
}

func (m *repoModel) countChecked() int {
	n := 0
	for _, s := range m.states {
		if s.checked {
			n++
		}
	}
	return n
}

// refreshTitle keeps the selection count pinned in the list header, visible
// on every page and under any filter — the state the picker will confirm
// must never be less visible than the rows on screen.
func (m *repoModel) refreshTitle() {
	m.list.Title = fmt.Sprintf("Pick repos · %d/%d selected", m.countChecked(), len(m.states))
}

func (m *repoModel) Init() tea.Cmd { return nil }

func (m *repoModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.list.SetSize(msg.Width, msg.Height-2)
	case tea.KeyMsg:
		// While the filter input is focused, every key belongs to it.
		if m.list.FilterState() == list.Filtering {
			break
		}
		switch msg.String() {
		case "ctrl+c", "esc", "q":
			m.cancelled = true
			return m, tea.Quit
		case " ", "x":
			if it, ok := m.list.SelectedItem().(repoItem); ok {
				it.s.checked = !it.s.checked
				m.refreshTitle()
			}
			return m, nil
		case "a", "n":
			// Operates on the visible items, so a filtered "a" selects the
			// narrowed set rather than everything.
			for _, li := range m.list.VisibleItems() {
				if it, ok := li.(repoItem); ok {
					it.s.checked = msg.String() == "a"
				}
			}
			m.refreshTitle()
			return m, nil
		case "enter":
			if m.countChecked() == 0 {
				return m, m.list.NewStatusMessage("select at least one repo (space) or press q to cancel")
			}
			m.confirmed = true
			return m, tea.Quit
		}
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m *repoModel) View() string {
	if m.confirmed || m.cancelled {
		return ""
	}
	return m.list.View()
}

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
	out_ := make([]string, 0, len(rm.states))
	for _, s := range rm.states {
		if s.checked {
			out_ = append(out_, s.name)
		}
	}
	return out_, false, nil
}
