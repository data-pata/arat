package tui

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/data-pata/arat/internal/workspace"
)

func newRepoTestModel(items ...workspace.RepoCandidate) *repoModel {
	m := newRepoModel(items)
	// A list only renders rows once it has a size; simulate the WindowSizeMsg
	// bubbletea sends on startup.
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	return next.(*repoModel)
}

func TestRepoModel_initialChecksMirrorCandidates(t *testing.T) {
	m := newRepoTestModel(
		workspace.RepoCandidate{Name: "a", Selected: true},
		workspace.RepoCandidate{Name: "b", Selected: false},
		workspace.RepoCandidate{Name: "c", Selected: true},
	)
	assert.Equal(t, 2, m.countChecked())
	assert.True(t, m.states[0].checked)
	assert.False(t, m.states[1].checked)
	assert.True(t, m.states[2].checked)
}

func TestRepoModel_spaceTogglesCurrent(t *testing.T) {
	m := newRepoTestModel(
		workspace.RepoCandidate{Name: "a", Selected: true},
		workspace.RepoCandidate{Name: "b", Selected: false},
	)
	// Cursor starts at 0; space should uncheck "a".
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	rm := updated.(*repoModel)
	assert.False(t, rm.states[0].checked)
	// Down + space toggles "b" on.
	updated, _ = rm.Update(tea.KeyMsg{Type: tea.KeyDown})
	rm = updated.(*repoModel)
	updated, _ = rm.Update(tea.KeyMsg{Type: tea.KeySpace})
	rm = updated.(*repoModel)
	assert.True(t, rm.states[1].checked)
}

func TestRepoModel_aSelectsAllNDeselects(t *testing.T) {
	m := newRepoTestModel(
		workspace.RepoCandidate{Name: "a"},
		workspace.RepoCandidate{Name: "b"},
		workspace.RepoCandidate{Name: "c"},
	)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	rm := updated.(*repoModel)
	assert.Equal(t, 3, rm.countChecked())
	updated, _ = rm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	rm = updated.(*repoModel)
	assert.Equal(t, 0, rm.countChecked())
}

func TestRepoModel_enterRefusesEmptySelection(t *testing.T) {
	m := newRepoTestModel(workspace.RepoCandidate{Name: "a", Selected: false})
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	rm := updated.(*repoModel)
	assert.False(t, rm.confirmed, "must not confirm an empty selection")
	assert.NotNil(t, cmd, "a status message cmd is emitted, not a quit")
}

func TestRepoModel_enterConfirmsWithSelection(t *testing.T) {
	m := newRepoTestModel(workspace.RepoCandidate{Name: "a", Selected: true})
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	rm := updated.(*repoModel)
	require.NotNil(t, cmd, "should emit a quit cmd")
	assert.True(t, rm.confirmed)
	assert.False(t, rm.cancelled)
}

func TestRepoModel_qCancelsRegardlessOfSelection(t *testing.T) {
	m := newRepoTestModel(workspace.RepoCandidate{Name: "a", Selected: true})
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	rm := updated.(*repoModel)
	require.NotNil(t, cmd)
	assert.True(t, rm.cancelled)
	assert.False(t, rm.confirmed)
}

func TestRepoModel_escAndCtrlCCancel(t *testing.T) {
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEsc}, {Type: tea.KeyCtrlC}} {
		m := newRepoTestModel(workspace.RepoCandidate{Name: "a"})
		updated, cmd := m.Update(key)
		rm := updated.(*repoModel)
		require.NotNil(t, cmd)
		assert.True(t, rm.cancelled)
	}
}

func TestRepoModel_titleTracksSelectionCount(t *testing.T) {
	m := newRepoTestModel(
		workspace.RepoCandidate{Name: "a", Selected: true},
		workspace.RepoCandidate{Name: "b", Selected: false},
	)
	assert.Contains(t, m.list.Title, "1/2 selected")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	rm := updated.(*repoModel)
	assert.Contains(t, rm.list.Title, "0/2 selected")
}

func TestRepoModel_filteredToggleMutatesUnderlyingState(t *testing.T) {
	m := newRepoTestModel(
		workspace.RepoCandidate{Name: "retail-mono", Selected: false},
		workspace.RepoCandidate{Name: "kiab", Selected: false},
	)
	// Apply a filter (SetFilterText is the synchronous path; keystroke
	// filtering resolves through async tea.Cmds a unit test can't pump),
	// then toggle the surviving row.
	m.list.SetFilterText("kiab")
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	rm := next.(*repoModel)
	assert.True(t, rm.states[1].checked, "toggling the filtered row checks kiab")
	assert.False(t, rm.states[0].checked, "retail-mono untouched")
}

func TestRepoModel_selectAllRespectsFilter(t *testing.T) {
	m := newRepoTestModel(
		workspace.RepoCandidate{Name: "retail-mono"},
		workspace.RepoCandidate{Name: "retail-bruno"},
		workspace.RepoCandidate{Name: "kiab"},
	)
	m.list.SetFilterText("retail")
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	rm := next.(*repoModel)
	assert.Equal(t, 2, rm.countChecked(), "a selects only the filtered subset")
	assert.False(t, rm.states[2].checked, "kiab stays unchecked")
}

func TestRepoModel_view(t *testing.T) {
	m := newRepoTestModel(
		workspace.RepoCandidate{Name: "alpha", Selected: true, Source: "default_repos"},
		workspace.RepoCandidate{Name: "beta", Selected: false, Source: "other clone"},
	)
	v := m.View()
	assert.Contains(t, v, "alpha")
	assert.Contains(t, v, "beta")
	assert.Contains(t, v, "[x]")
	assert.Contains(t, v, "[ ]")
	assert.Contains(t, v, "default_repos")
	assert.Contains(t, v, "selected")

	// View returns empty after confirmation/cancellation so the picker doesn't
	// leave its UI on screen.
	m.confirmed = true
	assert.Empty(t, m.View())
	m.confirmed = false
	m.cancelled = true
	assert.Empty(t, m.View())
}

func TestPickRepos_emptyCandidatesError(t *testing.T) {
	_, _, err := PickRepos(context.Background(), nil, nil)
	require.Error(t, err)
}

func TestRepoModel_init(t *testing.T) {
	m := newRepoTestModel(workspace.RepoCandidate{Name: "a"})
	assert.Nil(t, m.Init())
}
