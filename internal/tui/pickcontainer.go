package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/data-pata/arat/internal/linear"
)

// PickContainer opens an interactive picker over Linear projects and
// initiatives, for `arat project link` without an explicit --project /
// --initiative. Renders to `out` (typically stderr); reads from /dev/tty when
// available. Returns the chosen container, or nil if the user cancelled.
//
// Like PickWorkspace, delegates to fzf when it is on PATH and falls back to
// the in-process bubbletea picker otherwise.
func PickContainer(ctx context.Context, containers []linear.Container, out io.Writer) (*linear.Container, error) {
	if len(containers) == 0 {
		return nil, fmt.Errorf("no linear projects or initiatives to pick from")
	}
	if path, err := exec.LookPath("fzf"); err == nil {
		return pickContainerFzf(ctx, path, containers)
	}
	return pickContainerBubble(ctx, containers, out)
}

// pickContainerFzf renders the container picker via fzf. Lines carry a hidden
// index column (--with-nth hides it) so the selection maps back to the exact
// entry even when two containers share a name across kinds.
func pickContainerFzf(ctx context.Context, fzfPath string, containers []linear.Container) (*linear.Container, error) {
	var in bytes.Buffer
	for i, c := range containers {
		fmt.Fprintf(&in, "%d\t%s\t\x1b[2m%s %s\x1b[0m\n", i, c.Name, c.Kind, c.ID)
	}

	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, fzfPath,
		"--ansi",
		"--no-sort",
		"--reverse",
		"--height=~40%",
		"--delimiter=\t",
		"--with-nth=2..",
		"--prompt=arat project link ❯ ",
	)
	cmd.Stdin = &in
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// fzf exit codes: 1 = no match, 130 = cancelled (Ctrl+C / Esc).
			if code := ee.ExitCode(); code == 1 || code == 130 {
				return nil, nil
			}
		}
		return nil, fmt.Errorf("fzf: %w", err)
	}

	sel := strings.TrimRight(out.String(), "\n")
	if sel == "" {
		return nil, nil
	}
	idxStr, _, _ := strings.Cut(sel, "\t")
	idx, err := strconv.Atoi(idxStr)
	if err != nil || idx < 0 || idx >= len(containers) {
		return nil, fmt.Errorf("fzf returned unknown container line: %q", sel)
	}
	return &containers[idx], nil
}

func pickContainerBubble(ctx context.Context, containers []linear.Container, out io.Writer) (*linear.Container, error) {
	listItems := make([]list.Item, len(containers))
	for i, c := range containers {
		listItems[i] = containerItem(c)
	}

	delegate := list.NewDefaultDelegate()
	l := list.New(listItems, delegate, 0, 0)
	l.Title = "Link to a Linear project or initiative"
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(true)

	m := &containerModel{list: l}

	opts, cleanup := programOpts(ctx, out)
	defer cleanup()
	prog := tea.NewProgram(m, opts...)
	finalModel, err := prog.Run()
	if err != nil {
		return nil, fmt.Errorf("tui: %w", err)
	}
	return finalModel.(*containerModel).chosen, nil
}

// containerItem adapts a linear.Container to the list.Item interface.
type containerItem linear.Container

func (i containerItem) Title() string { return i.Name }

func (i containerItem) Description() string {
	parts := []string{dimStyle.Render(i.Kind)}
	if i.URL != "" {
		parts = append(parts, i.URL)
	}
	return strings.Join(parts, "  ")
}

// FilterValue includes the kind so typing "initiative" narrows to those.
func (i containerItem) FilterValue() string { return i.Name + " " + i.Kind }

type containerModel struct {
	list   list.Model
	chosen *linear.Container
}

func (m *containerModel) Init() tea.Cmd { return nil }

func (m *containerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
			if i, ok := m.list.SelectedItem().(containerItem); ok {
				c := linear.Container(i)
				m.chosen = &c
				return m, tea.Quit
			}
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m *containerModel) View() string {
	if m.chosen != nil {
		return ""
	}
	return m.list.View()
}
