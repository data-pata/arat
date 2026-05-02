package tui

import (
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/data-pata/arat/internal/workspace"
)

// These tests exercise the picker's model logic without spinning up a real
// bubbletea program — we drive Update directly, the same way bubbletea does
// at runtime, and assert state transitions.

func newTestModel(t *testing.T, items ...workspace.Workspace) *pickModel {
	t.Helper()
	listItems := make([]list.Item, len(items))
	for i, ws := range items {
		listItems[i] = wsItem(ws)
	}
	l := list.New(listItems, list.NewDefaultDelegate(), 60, 20)
	l.Title = "Pick a workspace"
	l.SetShowStatusBar(false)
	return &pickModel{list: l}
}

func TestPickModel_enterSelectsCurrent(t *testing.T) {
	m := newTestModel(t,
		workspace.Workspace{Name: "alpha", Path: "/a"},
		workspace.Workspace{Name: "beta", Path: "/b"},
	)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd, "enter should emit a quit command")
	pm := updated.(*pickModel)
	require.NotNil(t, pm.chosen)
	assert.Equal(t, "alpha", pm.chosen.Name)
}

func TestPickModel_qQuitsWithoutChoice(t *testing.T) {
	m := newTestModel(t, workspace.Workspace{Name: "alpha", Path: "/a"})
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	require.NotNil(t, cmd)
	pm := updated.(*pickModel)
	assert.Nil(t, pm.chosen, "quitting must not record a choice")
}

func TestPickModel_escQuitsWithoutChoice(t *testing.T) {
	m := newTestModel(t, workspace.Workspace{Name: "alpha", Path: "/a"})
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	require.NotNil(t, cmd)
	pm := updated.(*pickModel)
	assert.Nil(t, pm.chosen)
}

func TestPickModel_ctrlCQuitsWithoutChoice(t *testing.T) {
	m := newTestModel(t, workspace.Workspace{Name: "alpha", Path: "/a"})
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	require.NotNil(t, cmd)
	pm := updated.(*pickModel)
	assert.Nil(t, pm.chosen)
}

func TestPickModel_arrowDownThenEnter(t *testing.T) {
	m := newTestModel(t,
		workspace.Workspace{Name: "alpha", Path: "/a"},
		workspace.Workspace{Name: "beta", Path: "/b"},
	)
	// Move down once.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(*pickModel)
	// Then enter.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	pm := next.(*pickModel)
	require.NotNil(t, pm.chosen)
	assert.Equal(t, "beta", pm.chosen.Name)
}

func TestPickModel_windowSize(t *testing.T) {
	m := newTestModel(t, workspace.Workspace{Name: "alpha", Path: "/a"})
	_, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	// No assertion on internal sizes — we just verify it doesn't panic and
	// the model is still usable.
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd)
	pm := updated.(*pickModel)
	require.NotNil(t, pm.chosen)
}

func TestPickModel_view(t *testing.T) {
	m := newTestModel(t, workspace.Workspace{Name: "alpha", Path: "/a"})
	v := m.View()
	assert.Contains(t, v, "alpha")

	// After choosing, view returns empty.
	chosen := workspace.Workspace{Name: "alpha"}
	m.chosen = &chosen
	assert.Empty(t, m.View())
}

func TestWsItem_titleDescriptionFilter(t *testing.T) {
	w := workspace.Workspace{
		Name:   "abc-1--foo",
		Ticket: "abc-1",
		Repos: []workspace.RepoStatus{
			{Name: "a", Dirty: true},
			{Name: "b", Stashes: 2},
		},
	}
	item := wsItem(w)
	assert.Equal(t, "abc-1--foo", item.Title())
	desc := item.Description()
	assert.Contains(t, desc, "abc-1")
	assert.Contains(t, desc, "2 repos")
	assert.Contains(t, desc, "dirty:1")
	assert.Contains(t, desc, "stashes:2")
	assert.Contains(t, item.FilterValue(), "abc-1--foo")
	assert.Contains(t, item.FilterValue(), "abc-1")
}

func TestWsItem_descriptionNoFlags(t *testing.T) {
	w := workspace.Workspace{Name: "x", Repos: []workspace.RepoStatus{{Name: "r"}}}
	item := wsItem(w)
	desc := item.Description()
	assert.Equal(t, "1 repo", desc) // singular, no flags
}

func TestPickWorkspace_emptyError(t *testing.T) {
	_, err := PickWorkspace(t.Context(), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no workspaces")
}

func TestPickModel_init(t *testing.T) {
	m := newTestModel(t, workspace.Workspace{Name: "x"})
	assert.Nil(t, m.Init())
}

func TestSummarizeFlags_allWarnings(t *testing.T) {
	w := workspace.Workspace{Repos: []workspace.RepoStatus{
		{Dirty: true, Unpushed: true, Stashes: 1},
		{Dirty: true},
	}}
	out := summarizeFlags(w)
	assert.Contains(t, out, "dirty:2")
	assert.Contains(t, out, "unpushed:1")
	assert.Contains(t, out, "stashes:1")
}
