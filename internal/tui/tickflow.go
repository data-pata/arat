package tui

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/data-pata/arat/internal/linear"
)

// TicketAction is what the user picked in the action-chooser TUI.
type TicketAction int

const (
	ActionCancelled TicketAction = iota // user pressed q/Esc/Ctrl+C
	ActionSkip                          // proceed without a ticket
	ActionPick                          // pick from an existing list
	ActionCreate                        // tell the user to run `arat ticket create` first
)

// TicketFlowResult is the outcome of the whole interactive ticket flow.
type TicketFlowResult struct {
	Action   TicketAction
	IssueID  string // populated when Action == ActionPick and a ticket was selected
	HintText string // when Action == ActionCreate, a one-line hint to print to stderr
}

// issuePicker is the type of pickIssue, lifted out for tests.
type issuePicker func(ctx context.Context, issues []linear.Issue, out io.Writer) (*linear.Issue, error)

// PickTicketFlow runs the full interactive ticket flow. See dispatch for the
// state-machine; this thin entrypoint wires the real action-chooser and
// issue picker.
func PickTicketFlow(ctx context.Context, lc linear.Reader, team string, out io.Writer) (TicketFlowResult, error) {
	action, err := pickTicketAction(ctx, out)
	if err != nil {
		return TicketFlowResult{}, err
	}
	return dispatchAction(ctx, action, lc, team, pickIssue, out)
}

// dispatchAction is the pure (testable) state-machine that turns a chosen
// TicketAction into a final result, fetching issues and consulting the
// issue picker as needed.
func dispatchAction(ctx context.Context, action TicketAction, lc linear.Reader, team string, pick issuePicker, out io.Writer) (TicketFlowResult, error) {
	switch action {
	case ActionCancelled, ActionSkip:
		return TicketFlowResult{Action: action}, nil
	case ActionCreate:
		return TicketFlowResult{
			Action:   ActionCreate,
			HintText: "run `arat ticket create -t \"<title>\"` first, then re-run with --ticket <id>",
		}, nil
	case ActionPick:
		issues, err := lc.IssueList(ctx, linear.IssueListOptions{AssignedToMe: true, Team: team})
		if err != nil {
			return TicketFlowResult{}, fmt.Errorf("list issues: %w", err)
		}
		if len(issues) == 0 {
			return TicketFlowResult{
				Action:   ActionCreate,
				HintText: "no open issues assigned to you — create one with `arat ticket create -t \"<title>\"` and re-run with --ticket <id>",
			}, nil
		}
		picked, err := pick(ctx, issues, out)
		if err != nil {
			return TicketFlowResult{}, err
		}
		if picked == nil {
			return TicketFlowResult{Action: ActionCancelled}, nil
		}
		return TicketFlowResult{Action: ActionPick, IssueID: picked.ID}, nil
	}
	return TicketFlowResult{Action: ActionCancelled}, nil
}

// --- action chooser model -------------------------------------------

type actionItem struct {
	label  string
	desc   string
	action TicketAction
}

func (a actionItem) Title() string       { return a.label }
func (a actionItem) Description() string { return a.desc }
func (a actionItem) FilterValue() string { return a.label }

type actionModel struct {
	list     list.Model
	chosen   TicketAction
	resolved bool
}

func newActionModel() *actionModel {
	items := []list.Item{
		actionItem{"Skip ticket", "create the workspace without a ticket attached", ActionSkip},
		actionItem{"Pick existing", "choose from your open Linear issues", ActionPick},
		actionItem{"Create new", "exit and tell me how to create one", ActionCreate},
	}
	delegate := list.NewDefaultDelegate()
	l := list.New(items, delegate, 0, 0)
	l.Title = "Attach a ticket?"
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(false)
	l.SetShowHelp(true)
	return &actionModel{list: l}
}

func (m *actionModel) Init() tea.Cmd { return nil }

func (m *actionModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.list.SetSize(msg.Width, msg.Height-2)
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc", "q":
			m.chosen = ActionCancelled
			m.resolved = true
			return m, tea.Quit
		case "enter":
			if it, ok := m.list.SelectedItem().(actionItem); ok {
				m.chosen = it.action
				m.resolved = true
				return m, tea.Quit
			}
		}
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m *actionModel) View() string {
	if m.resolved {
		return ""
	}
	return m.list.View()
}

func pickTicketAction(ctx context.Context, out io.Writer) (TicketAction, error) {
	m := newActionModel()
	prog := tea.NewProgram(m, programOpts(ctx, out)...)
	final, err := prog.Run()
	if err != nil {
		return ActionCancelled, fmt.Errorf("tui: %w", err)
	}
	return final.(*actionModel).chosen, nil
}

// --- issue picker model ---------------------------------------------

type issueItem struct{ iss linear.Issue }

func (i issueItem) Title() string       { return i.iss.ID + "  " + i.iss.Title }
func (i issueItem) Description() string { return dimStyle.Render(i.iss.State) }
func (i issueItem) FilterValue() string { return i.iss.ID + " " + i.iss.Title }

type issueModel struct {
	list   list.Model
	chosen *linear.Issue
}

func newIssueModel(issues []linear.Issue) *issueModel {
	items := make([]list.Item, len(issues))
	for i, iss := range issues {
		items[i] = issueItem{iss: iss}
	}
	delegate := list.NewDefaultDelegate()
	l := list.New(items, delegate, 0, 0)
	l.Title = "Pick a ticket"
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	return &issueModel{list: l}
}

func (m *issueModel) Init() tea.Cmd { return nil }

func (m *issueModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.list.SetSize(msg.Width, msg.Height-2)
	case tea.KeyMsg:
		if m.list.FilterState() == list.Filtering {
			break
		}
		switch msg.String() {
		case "ctrl+c", "esc", "q":
			return m, tea.Quit
		case "enter":
			if it, ok := m.list.SelectedItem().(issueItem); ok {
				iss := it.iss
				m.chosen = &iss
				return m, tea.Quit
			}
		}
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m *issueModel) View() string {
	if m.chosen != nil {
		return ""
	}
	return m.list.View()
}

func pickIssue(ctx context.Context, issues []linear.Issue, out io.Writer) (*linear.Issue, error) {
	m := newIssueModel(issues)
	prog := tea.NewProgram(m, programOpts(ctx, out)...)
	final, err := prog.Run()
	if err != nil {
		return nil, fmt.Errorf("tui: %w", err)
	}
	return final.(*issueModel).chosen, nil
}

// programOpts wires output to the given writer and input to /dev/tty when
// available (so the picker works inside `$( ... )` substitutions).
func programOpts(ctx context.Context, out io.Writer) []tea.ProgramOption {
	opts := []tea.ProgramOption{
		tea.WithOutput(out),
		tea.WithContext(ctx),
	}
	if tty, err := os.Open("/dev/tty"); err == nil {
		opts = append(opts, tea.WithInput(tty))
	}
	return opts
}
