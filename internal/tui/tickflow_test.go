package tui

import (
	"context"
	"errors"
	"io"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/data-pata/arat/internal/linear"
)

func TestActionModel_enterSelectsCurrent(t *testing.T) {
	m := newActionModel()
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd)
	am := updated.(*actionModel)
	assert.True(t, am.resolved)
	assert.Equal(t, ActionSkip, am.chosen)
}

func TestActionModel_arrowDownThenEnter(t *testing.T) {
	m := newActionModel()
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(*actionModel)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	am := next.(*actionModel)
	assert.Equal(t, ActionPick, am.chosen)
}

func TestActionModel_quitCancels(t *testing.T) {
	m := newActionModel()
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	require.NotNil(t, cmd)
	am := updated.(*actionModel)
	assert.Equal(t, ActionCancelled, am.chosen)
}

func TestActionModel_view(t *testing.T) {
	m := newActionModel()
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	v := m.View()
	assert.Contains(t, v, "Skip ticket")
	assert.Contains(t, v, "Pick existing")
	assert.Contains(t, v, "Create new")

	m.resolved = true
	assert.Empty(t, m.View())
}

func TestActionModel_windowSize(t *testing.T) {
	m := newActionModel()
	_, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	assert.True(t, updated.(*actionModel).resolved)
}

func TestIssueModel_enterSelects(t *testing.T) {
	m := newIssueModel([]linear.Issue{
		{ID: "ABC-1", Title: "first", State: "Backlog"},
		{ID: "ABC-2", Title: "second", State: "Started"},
	})
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd)
	im := updated.(*issueModel)
	require.NotNil(t, im.chosen)
	assert.Equal(t, "ABC-1", im.chosen.ID)
}

func TestIssueModel_quitCancels(t *testing.T) {
	m := newIssueModel([]linear.Issue{{ID: "ABC-1"}})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	assert.Nil(t, updated.(*issueModel).chosen)
}

func TestIssueItem_titleAndFilter(t *testing.T) {
	it := issueItem{iss: linear.Issue{ID: "ABC-1", Title: "Fix it", State: "Backlog"}}
	assert.Contains(t, it.Title(), "ABC-1")
	assert.Contains(t, it.Title(), "Fix it")
	assert.Contains(t, it.Description(), "Backlog")
	assert.Contains(t, it.FilterValue(), "ABC-1")
	assert.Contains(t, it.FilterValue(), "Fix it")
}

// --- dispatchAction (testable state machine) ------------------------

type fakeReader struct {
	issues []linear.Issue
	err    error
	calls  []linear.IssueListOptions
}

func (f *fakeReader) IssueList(_ context.Context, opts linear.IssueListOptions) ([]linear.Issue, error) {
	f.calls = append(f.calls, opts)
	return f.issues, f.err
}

// fixedTitle returns a composePrompt that always yields the given title and
// no description.
func fixedTitle(title string) composePrompt {
	return func(context.Context, io.Writer) (composeResult, error) {
		return composeResult{Title: title}, nil
	}
}

// fixedCompose returns a composePrompt yielding both title and description.
func fixedCompose(title, desc string) composePrompt {
	return func(context.Context, io.Writer) (composeResult, error) {
		return composeResult{Title: title, Description: desc}, nil
	}
}

// cancelledCompose returns a composePrompt that simulates Esc/Ctrl+C.
func cancelledCompose() composePrompt {
	return func(context.Context, io.Writer) (composeResult, error) {
		return composeResult{Cancelled: true}, nil
	}
}

// failingTitle returns a composePrompt that fails the test if invoked.
func failingTitle(t *testing.T) composePrompt {
	return func(context.Context, io.Writer) (composeResult, error) {
		t.Helper()
		t.Fatal("compose prompt must not be invoked in this scenario")
		return composeResult{}, nil
	}
}

func TestDispatch_skipReturnsSkip(t *testing.T) {
	got, err := dispatchAction(t.Context(), ActionSkip, &fakeReader{}, "ABC", nil, failingTitle(t), io.Discard)
	require.NoError(t, err)
	assert.Equal(t, ActionSkip, got.Action)
	assert.Empty(t, got.IssueID)
}

func TestDispatch_cancelledReturnsCancelled(t *testing.T) {
	got, err := dispatchAction(t.Context(), ActionCancelled, &fakeReader{}, "ABC", nil, failingTitle(t), io.Discard)
	require.NoError(t, err)
	assert.Equal(t, ActionCancelled, got.Action)
}

func TestDispatch_createPromptsForTitle(t *testing.T) {
	got, err := dispatchAction(t.Context(), ActionCreate, &fakeReader{}, "ABC", nil, fixedTitle("Fix flush race"), io.Discard)
	require.NoError(t, err)
	assert.Equal(t, ActionCreate, got.Action)
	assert.Equal(t, "Fix flush race", got.NewTitle)
}

func TestDispatch_createEmptyTitleCancels(t *testing.T) {
	got, err := dispatchAction(t.Context(), ActionCreate, &fakeReader{}, "ABC", nil, fixedTitle("   "), io.Discard)
	require.NoError(t, err)
	assert.Equal(t, ActionCancelled, got.Action, "whitespace-only title is treated as cancel")
}

func TestDispatch_createTitlePromptError(t *testing.T) {
	prompt := func(context.Context, io.Writer) (composeResult, error) {
		return composeResult{}, errors.New("tui broke")
	}
	_, err := dispatchAction(t.Context(), ActionCreate, &fakeReader{}, "ABC", nil, prompt, io.Discard)
	require.Error(t, err)
}

func TestDispatch_createCarriesDescription(t *testing.T) {
	got, err := dispatchAction(t.Context(), ActionCreate, &fakeReader{}, "ABC", nil, fixedCompose("Title", "  multi\nline body  "), io.Discard)
	require.NoError(t, err)
	assert.Equal(t, ActionCreate, got.Action)
	assert.Equal(t, "Title", got.NewTitle)
	assert.Equal(t, "multi\nline body", got.NewDescription, "description is trimmed of surrounding whitespace")
}

func TestDispatch_createCancelledByPrompt(t *testing.T) {
	got, err := dispatchAction(t.Context(), ActionCreate, &fakeReader{}, "ABC", nil, cancelledCompose(), io.Discard)
	require.NoError(t, err)
	assert.Equal(t, ActionCancelled, got.Action)
}

func TestDispatch_pickFetchesAndPicks(t *testing.T) {
	lr := &fakeReader{issues: []linear.Issue{{ID: "ABC-1"}, {ID: "ABC-2"}}}
	picker := func(_ context.Context, items []linear.Issue, _ io.Writer) (*linear.Issue, error) {
		assert.Len(t, items, 2)
		chosen := items[1]
		return &chosen, nil
	}
	got, err := dispatchAction(t.Context(), ActionPick, lr, "ABC", picker, failingTitle(t), io.Discard)
	require.NoError(t, err)
	assert.Equal(t, ActionPick, got.Action)
	assert.Equal(t, "ABC-2", got.IssueID)

	require.Len(t, lr.calls, 1)
	assert.True(t, lr.calls[0].AssignedToMe)
	assert.Equal(t, "ABC", lr.calls[0].Team)
}

func TestDispatch_pickEmptyListFallsThroughToCreate(t *testing.T) {
	lr := &fakeReader{issues: nil}
	picker := func(context.Context, []linear.Issue, io.Writer) (*linear.Issue, error) {
		t.Fatal("picker must not be called when there are no issues")
		return nil, nil
	}
	got, err := dispatchAction(t.Context(), ActionPick, lr, "ABC", picker, fixedTitle("Quick fix"), io.Discard)
	require.NoError(t, err)
	assert.Equal(t, ActionCreate, got.Action)
	assert.Equal(t, "Quick fix", got.NewTitle)
}

func TestDispatch_pickEmptyListCancelTitle(t *testing.T) {
	lr := &fakeReader{issues: nil}
	got, err := dispatchAction(t.Context(), ActionPick, lr, "ABC", nil, fixedTitle(""), io.Discard)
	require.NoError(t, err)
	assert.Equal(t, ActionCancelled, got.Action)
}

func TestDispatch_pickListError(t *testing.T) {
	lr := &fakeReader{err: errors.New("boom")}
	_, err := dispatchAction(t.Context(), ActionPick, lr, "ABC", nil, failingTitle(t), io.Discard)
	require.Error(t, err)
}

func TestDispatch_pickerCancels(t *testing.T) {
	lr := &fakeReader{issues: []linear.Issue{{ID: "ABC-1"}}}
	picker := func(context.Context, []linear.Issue, io.Writer) (*linear.Issue, error) {
		return nil, nil // user pressed q/Esc
	}
	got, err := dispatchAction(t.Context(), ActionPick, lr, "ABC", picker, failingTitle(t), io.Discard)
	require.NoError(t, err)
	assert.Equal(t, ActionCancelled, got.Action)
}

func TestDispatch_pickerError(t *testing.T) {
	lr := &fakeReader{issues: []linear.Issue{{ID: "ABC-1"}}}
	picker := func(context.Context, []linear.Issue, io.Writer) (*linear.Issue, error) {
		return nil, errors.New("tui broke")
	}
	_, err := dispatchAction(t.Context(), ActionPick, lr, "ABC", picker, failingTitle(t), io.Discard)
	require.Error(t, err)
}

func TestActionItemFilterValue(t *testing.T) {
	it := actionItem{label: "Skip ticket"}
	assert.Equal(t, "Skip ticket", it.FilterValue())
}

// --- composeModel ---------------------------------------------------

func TestComposeModel_enterAdvancesFromTitleToDescription(t *testing.T) {
	m := newComposeModel()
	assert.Equal(t, 0, m.focus, "starts focused on title")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	cm := updated.(*composeModel)
	assert.Equal(t, 1, cm.focus, "enter on title moves focus to description")
	assert.False(t, cm.done)
}

func TestComposeModel_ctrlDSubmits(t *testing.T) {
	m := newComposeModel()
	m.title.SetValue("Title")
	m.desc.SetValue("body")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	require.NotNil(t, cmd)
	cm := updated.(*composeModel)
	assert.True(t, cm.done)
	assert.False(t, cm.cancelled)
	assert.Equal(t, "Title", cm.title.Value())
	assert.Equal(t, "body", cm.desc.Value())
}

func TestComposeModel_escCancels(t *testing.T) {
	m := newComposeModel()
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	cm := updated.(*composeModel)
	assert.True(t, cm.cancelled)
	assert.True(t, cm.done)
}

func TestComposeModel_tabTogglesFocus(t *testing.T) {
	m := newComposeModel()
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	cm := updated.(*composeModel)
	assert.Equal(t, 1, cm.focus)
	updated, _ = cm.Update(tea.KeyMsg{Type: tea.KeyTab})
	cm = updated.(*composeModel)
	assert.Equal(t, 0, cm.focus)
}

func TestComposeModel_view(t *testing.T) {
	m := newComposeModel()
	v := m.View()
	assert.Contains(t, v, "Title:")
	assert.Contains(t, v, "Description:")
	assert.Contains(t, v, "ctrl+d")

	m.done = true
	assert.Empty(t, m.View())
}

func TestComposeModel_init(t *testing.T) {
	// Init returns a non-nil command (textinput's cursor blink).
	assert.NotNil(t, newComposeModel().Init())
}

func TestActionAndIssueModelInit(t *testing.T) {
	assert.Nil(t, newActionModel().Init())
	assert.Nil(t, newIssueModel(nil).Init())
}

func TestIssueModel_view(t *testing.T) {
	m := newIssueModel([]linear.Issue{{ID: "ABC-1", Title: "x"}})
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	v := m.View()
	assert.Contains(t, v, "ABC-1")

	chosen := linear.Issue{ID: "ABC-1"}
	m.chosen = &chosen
	assert.Empty(t, m.View())
}
