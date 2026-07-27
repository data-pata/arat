package tui

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// nameModel is the one-field workspace-name prompt `arat new` opens when no
// name argument was given. The input arrives pre-filled with the slug derived
// from the chosen issue's title (empty when the ticket was skipped), so the
// common interaction is a single Enter.
type nameModel struct {
	input     textinput.Model
	ticket    string // shown in the hint so the final directory name is predictable
	cancelled bool
	done      bool
}

func newNameModel(def, ticket string) *nameModel {
	ti := textinput.New()
	ti.Placeholder = "workspace name"
	ti.CharLimit = 64
	ti.Width = 60
	ti.SetValue(def)
	ti.CursorEnd()
	ti.Focus()
	return &nameModel{input: ti, ticket: ticket}
}

func (m *nameModel) Init() tea.Cmd { return textinput.Blink }

func (m *nameModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "ctrl+c", "esc":
			m.cancelled = true
			m.done = true
			return m, tea.Quit
		case "enter":
			m.done = true
			return m, tea.Quit
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *nameModel) View() string {
	if m.done {
		return ""
	}
	hint := "directory: <name>"
	if m.ticket != "" {
		hint = fmt.Sprintf("directory: %s--<name>", m.ticket)
	}
	return strings.Join([]string{
		"Workspace name",
		"",
		m.input.View(),
		"",
		dimStyle.Render("(" + hint + " · enter accepts · esc cancels)"),
	}, "\n")
}

// AskName prompts for a workspace short name, pre-filled with def. Returns
// the entered name (trimmed; may be empty — the caller decides what an empty
// answer means) and whether the user cancelled outright.
func AskName(ctx context.Context, def, ticket string, out io.Writer) (string, bool, error) {
	m := newNameModel(def, ticket)
	opts, cleanup := programOpts(ctx, out)
	defer cleanup()
	prog := tea.NewProgram(m, opts...)
	final, err := prog.Run()
	if err != nil {
		return "", false, fmt.Errorf("tui: %w", err)
	}
	nm := final.(*nameModel)
	if nm.cancelled {
		return "", true, nil
	}
	return strings.TrimSpace(nm.input.Value()), false, nil
}
