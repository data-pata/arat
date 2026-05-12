package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/data-pata/arat/internal/workspace"
)

// pickWorkspaceFzf renders the workspace picker via fzf. fzf opens /dev/tty
// directly for its UI, so colors work regardless of how the caller redirected
// stdout — solving the shell-wrapper $(...) problem we can't fully solve in
// the bubbletea path.
//
// Items are written in caller-supplied order; we pass --no-sort so fzf
// preserves it (the caller sorts by mtime, most-recent first).
//
// Returns nil if the user cancelled or fzf had no match.
func pickWorkspaceFzf(ctx context.Context, fzfPath string, items []workspace.Workspace) (*workspace.Workspace, error) {
	var in bytes.Buffer
	for _, ws := range items {
		in.WriteString(ws.Name)
		if ws.Ticket != "" {
			// \t separates name (column 1) from ticket (column 2); the dim
			// ANSI wraps the ticket so it reads as secondary info. fzf's
			// --ansi strips the codes for matching but renders them on screen.
			in.WriteString("\t\x1b[2m")
			in.WriteString(ws.Ticket)
			in.WriteString("\x1b[0m")
		}
		in.WriteByte('\n')
	}

	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, fzfPath,
		"--ansi",
		"--no-sort",
		"--reverse",
		"--height=~40%",
		"--delimiter=\t",
		"--prompt=arat go ❯ ",
	)
	cmd.Stdin = &in
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// fzf exit codes: 1 = no match, 130 = cancelled (Ctrl+C / Esc).
			// Both mean "no selection", not an error.
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
	// Line is "<name>\t<ticket-with-ansi>" or just "<name>".
	name := sel
	if i := strings.IndexByte(sel, '\t'); i >= 0 {
		name = sel[:i]
	}
	for i := range items {
		if items[i].Name == name {
			return &items[i], nil
		}
	}
	return nil, fmt.Errorf("fzf returned unknown workspace: %q", name)
}
