package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/data-pata/arat/internal/linear"
)

// TicketAction is what the user picked in the action-chooser TUI.
type TicketAction int

const (
	ActionCancelled TicketAction = iota // user pressed q/Esc/Ctrl+C
	ActionSkip                          // proceed without a ticket
	ActionPick                          // pick from an existing list
	ActionCreate                        // create a new ticket from a typed title
)

// TicketFlowResult is the outcome of the whole interactive ticket flow.
type TicketFlowResult struct {
	Action         TicketAction
	IssueID        string // populated when Action == ActionPick and a ticket was selected
	IssueTitle     string // the picked issue's title, for deriving a workspace name
	NewTitle       string // populated when Action == ActionCreate; cmd will shell out to linear
	NewDescription string // optional description supplied alongside NewTitle
}

// issuePicker is the type of pickIssue, lifted out for tests.
type issuePicker func(ctx context.Context, issues []linear.Issue, out io.Writer) (*linear.Issue, error)

// composeResult is what the title+description prompt returns.
type composeResult struct {
	Title       string
	Description string
	Cancelled   bool
}

// composePrompt is the type of askCompose, lifted out for tests.
type composePrompt func(ctx context.Context, out io.Writer) (composeResult, error)

// TicketFlowOptions controls PickTicketFlow.
type TicketFlowOptions struct {
	Team string
	// AllowSkip offers a "skip" choice in the action chooser. `arat new` has
	// a meaningful skip (create the workspace ticketless); `arat attach` does
	// not — skipping there is just cancelling — so it hides the option.
	AllowSkip bool
}

// PickTicketFlow runs the full interactive ticket flow. See dispatch for the
// state-machine; this thin entrypoint wires the real action-chooser, issue
// picker, and compose prompt.
func PickTicketFlow(ctx context.Context, lc linear.Reader, opts TicketFlowOptions, out io.Writer) (TicketFlowResult, error) {
	action, err := pickTicketAction(ctx, opts.AllowSkip, out)
	if err != nil {
		return TicketFlowResult{}, err
	}
	return dispatchAction(ctx, action, lc, opts.Team, pickIssue, askCompose, out)
}

// dispatchAction is the pure (testable) state-machine that turns a chosen
// TicketAction into a final result, fetching issues, consulting the issue
// picker, or asking for a new ticket's title+description as needed.
func dispatchAction(ctx context.Context, action TicketAction, lc linear.Reader, team string, pick issuePicker, ask composePrompt, out io.Writer) (TicketFlowResult, error) {
	switch action {
	case ActionCancelled, ActionSkip:
		return TicketFlowResult{Action: action}, nil
	case ActionCreate:
		return promptCreate(ctx, ask, out)
	case ActionPick:
		issues, err := lc.IssueList(ctx, linear.IssueListOptions{AssignedToMe: true, Team: team})
		if err != nil {
			return TicketFlowResult{}, fmt.Errorf("list issues: %w", err)
		}
		if len(issues) == 0 {
			// Nothing assigned: fall through to the create prompt so the
			// user doesn't have to bail out and re-run.
			return promptCreate(ctx, ask, out)
		}
		picked, err := pick(ctx, issues, out)
		if err != nil {
			return TicketFlowResult{}, err
		}
		if picked == nil {
			return TicketFlowResult{Action: ActionCancelled}, nil
		}
		return TicketFlowResult{Action: ActionPick, IssueID: picked.ID, IssueTitle: picked.Title}, nil
	}
	return TicketFlowResult{Action: ActionCancelled}, nil
}

// promptCreate runs the compose prompt and turns its outcome into a flow
// result. An empty title (or Esc) is treated as cancellation.
func promptCreate(ctx context.Context, ask composePrompt, out io.Writer) (TicketFlowResult, error) {
	res, err := ask(ctx, out)
	if err != nil {
		return TicketFlowResult{}, err
	}
	if res.Cancelled {
		return TicketFlowResult{Action: ActionCancelled}, nil
	}
	title := strings.TrimSpace(res.Title)
	if title == "" {
		return TicketFlowResult{Action: ActionCancelled}, nil
	}
	return TicketFlowResult{
		Action:         ActionCreate,
		NewTitle:       title,
		NewDescription: strings.TrimSpace(res.Description),
	}, nil
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

func newActionModel(allowSkip bool) *actionModel {
	var items []list.Item
	if allowSkip {
		items = append(items, actionItem{"Skip ticket", "create the workspace without a ticket attached", ActionSkip})
	}
	items = append(items,
		actionItem{"Pick existing", "choose from your open Linear issues", ActionPick},
		actionItem{"Create new", "type a title (and optional description) to create one inline", ActionCreate},
	)
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

func pickTicketAction(ctx context.Context, allowSkip bool, out io.Writer) (TicketAction, error) {
	m := newActionModel(allowSkip)
	opts, cleanup := programOpts(ctx, out)
	defer cleanup()
	prog := tea.NewProgram(m, opts...)
	final, err := prog.Run()
	if err != nil {
		return ActionCancelled, fmt.Errorf("tui: %w", err)
	}
	return final.(*actionModel).chosen, nil
}

// --- new-ticket compose input ---------------------------------------

// composeModel is a two-field form: a single-line title (textinput) and a
// multi-line description (textarea). Tab cycles focus; Enter on the title
// advances to the description (and inserts a newline once focus is in the
// description); Ctrl+D submits; Esc/Ctrl+C cancels.
type composeModel struct {
	title     textinput.Model
	desc      textarea.Model
	focus     int // 0 = title, 1 = description
	cancelled bool
	done      bool
}

func newComposeModel() *composeModel {
	ti := textinput.New()
	ti.Placeholder = "ticket title"
	ti.CharLimit = 240
	ti.Width = 60
	ti.Focus()

	ta := textarea.New()
	ta.Placeholder = "description (optional)"
	ta.ShowLineNumbers = false
	ta.SetWidth(60)
	ta.SetHeight(6)
	ta.Blur()

	return &composeModel{title: ti, desc: ta}
}

func (m *composeModel) Init() tea.Cmd { return textinput.Blink }

func (m *composeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "ctrl+c", "esc":
			m.cancelled = true
			m.done = true
			return m, tea.Quit
		case "ctrl+d":
			m.done = true
			return m, tea.Quit
		case "tab", "shift+tab":
			m.toggleFocus()
			return m, nil
		case "enter":
			// On the title field, Enter advances to the description so the
			// user can type a body. Once focused on the description, Enter
			// falls through to the textarea (newline).
			if m.focus == 0 {
				m.focusDescription()
				return m, nil
			}
		}
	}
	var cmd tea.Cmd
	if m.focus == 0 {
		m.title, cmd = m.title.Update(msg)
	} else {
		m.desc, cmd = m.desc.Update(msg)
	}
	return m, cmd
}

func (m *composeModel) focusTitle() {
	m.focus = 0
	m.desc.Blur()
	m.title.Focus()
}

func (m *composeModel) focusDescription() {
	m.focus = 1
	m.title.Blur()
	m.desc.Focus()
}

func (m *composeModel) toggleFocus() {
	if m.focus == 0 {
		m.focusDescription()
	} else {
		m.focusTitle()
	}
}

func (m *composeModel) View() string {
	if m.done {
		return ""
	}
	return strings.Join([]string{
		"New ticket",
		"",
		"Title:",
		m.title.View(),
		"",
		"Description:",
		m.desc.View(),
		"",
		dimStyle.Render("(tab to switch field · ctrl+d submits · esc cancels)"),
	}, "\n")
}

func askCompose(ctx context.Context, out io.Writer) (composeResult, error) {
	m := newComposeModel()
	opts, cleanup := programOpts(ctx, out)
	defer cleanup()
	prog := tea.NewProgram(m, opts...)
	final, err := prog.Run()
	if err != nil {
		return composeResult{}, fmt.Errorf("tui: %w", err)
	}
	cm := final.(*composeModel)
	if cm.cancelled {
		return composeResult{Cancelled: true}, nil
	}
	return composeResult{Title: cm.title.Value(), Description: cm.desc.Value()}, nil
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
	opts, cleanup := programOpts(ctx, out)
	defer cleanup()
	prog := tea.NewProgram(m, opts...)
	final, err := prog.Run()
	if err != nil {
		return nil, fmt.Errorf("tui: %w", err)
	}
	return final.(*issueModel).chosen, nil
}

// programOpts wires output to the given writer and input to /dev/tty when
// available (so the picker works inside `$( ... )` substitutions). The
// returned cleanup closes the /dev/tty file descriptor; callers must defer
// it. cleanup is non-nil even when /dev/tty couldn't be opened.
func programOpts(ctx context.Context, out io.Writer) ([]tea.ProgramOption, func()) {
	// Point lipgloss's default renderer at our output writer so it probes the
	// right fd for color support. The shell wrapper (`arat init <shell>`) runs
	// `arat go` inside $(...), which makes stdout a pipe — and lipgloss's
	// default renderer probes os.Stdout, so it would decide "not a terminal"
	// and strip all color. Mutating the existing renderer's output (rather
	// than SetDefaultRenderer) is required because package-level styles and
	// bubbles' internal styles bind to the default renderer pointer at
	// NewStyle() time; replacing the pointer leaves them stale.
	lipgloss.DefaultRenderer().SetOutput(termenv.NewOutput(out))

	opts := []tea.ProgramOption{
		tea.WithOutput(out),
		tea.WithContext(ctx),
	}
	cleanup := func() {}
	if tty, err := os.Open("/dev/tty"); err == nil {
		opts = append(opts, tea.WithInput(tty))
		cleanup = func() { _ = tty.Close() }
	}
	return opts, cleanup
}
