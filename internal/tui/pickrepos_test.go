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
	return newRepoModel(items)
}

func TestRepoModel_initialChecksMirrorCandidates(t *testing.T) {
	m := newRepoTestModel(
		workspace.RepoCandidate{Name: "a", Selected: true},
		workspace.RepoCandidate{Name: "b", Selected: false},
		workspace.RepoCandidate{Name: "c", Selected: true},
	)
	assert.Equal(t, 2, m.countChecked())
	assert.True(t, m.items[0].checked)
	assert.False(t, m.items[1].checked)
	assert.True(t, m.items[2].checked)
}

func TestRepoModel_spaceTogglesCurrent(t *testing.T) {
	m := newRepoTestModel(
		workspace.RepoCandidate{Name: "a", Selected: true},
		workspace.RepoCandidate{Name: "b", Selected: false},
	)
	// Cursor starts at 0; space should uncheck "a".
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	rm := updated.(*repoModel)
	assert.False(t, rm.items[0].checked)
	// Down + space toggles "b" on.
	updated, _ = rm.Update(tea.KeyMsg{Type: tea.KeyDown})
	rm = updated.(*repoModel)
	updated, _ = rm.Update(tea.KeyMsg{Type: tea.KeySpace})
	rm = updated.(*repoModel)
	assert.True(t, rm.items[1].checked)
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
	assert.Nil(t, cmd, "must not quit on empty-confirm")
	assert.False(t, rm.confirmed)
	assert.NotEmpty(t, rm.hint, "should show a hint asking the user to select something")
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

func TestRepoModel_cursorBoundaries(t *testing.T) {
	m := newRepoTestModel(
		workspace.RepoCandidate{Name: "a"},
		workspace.RepoCandidate{Name: "b"},
	)
	// Up at top is a no-op (no panic, cursor stays at 0).
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	rm := updated.(*repoModel)
	assert.Equal(t, 0, rm.cursor)
	// Down to bottom, then Down again is a no-op.
	updated, _ = rm.Update(tea.KeyMsg{Type: tea.KeyDown})
	rm = updated.(*repoModel)
	assert.Equal(t, 1, rm.cursor)
	updated, _ = rm.Update(tea.KeyMsg{Type: tea.KeyDown})
	rm = updated.(*repoModel)
	assert.Equal(t, 1, rm.cursor)
}

func TestRepoModel_view(t *testing.T) {
	m := newRepoTestModel(
		workspace.RepoCandidate{Name: "alpha", Selected: true},
		workspace.RepoCandidate{Name: "beta", Selected: false},
	)
	v := m.View()
	assert.Contains(t, v, "alpha")
	assert.Contains(t, v, "beta")
	assert.Contains(t, v, "[x]")
	assert.Contains(t, v, "[ ]")
	assert.Contains(t, v, "space toggle")

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

func TestRepoModel_windowSize(t *testing.T) {
	m := newRepoTestModel(workspace.RepoCandidate{Name: "a"})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	rm := updated.(*repoModel)
	assert.Equal(t, 100, rm.width)
	assert.Equal(t, 40, rm.height)
}
