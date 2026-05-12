package cmd

import (
	"errors"
	"fmt"
	"sort"

	"github.com/data-pata/arat/internal/workspace"
	"github.com/spf13/cobra"
)

func newGoCmd(s *state) *cobra.Command {
	c := &cobra.Command{
		Use:   "go [name]",
		Short: "Print the path of a workspace (for shell-function cd integration)",
		Long: `Resolve a workspace and print its absolute path on stdout.

Pair with the shell integration from "arat init <shell>" so that
"arat go [name]" actually changes directory in your interactive shell:

  eval "$(arat init zsh)"   # in your rc
  arat go abc-123--postal  # cd directly by name
  arat go                   # opens an interactive picker

Without a name, opens an interactive picker (filterable list — type to
narrow, ↑/↓ to move, Enter to select, q/Esc/Ctrl+C to cancel). The
picker renders to stderr so it's safe to embed in $( ... ) — the chosen
path goes to stdout last.

Without a shell-function wrapper this command just prints the path —
useful in scripts:  cd "$(arat go abc-123--postal)".

The --print flag is accepted for forward compatibility; printing is the
default today.
`,
		Example: `  arat go                       # interactive picker
  arat go abc-123--postal      # by name
  cd "$(arat go abc-123--postal)"`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := s.loadConfig()
			if err != nil {
				return err
			}
			svc := s.deps.NewService(cfg)

			if len(args) == 0 {
				return s.runPicker(cmd, svc)
			}

			ws, err := svc.Get(cmd.Context(), args[0])
			if err != nil {
				if errors.Is(err, workspace.ErrNotFound) {
					return &exitErr{code: ExitNotFound, err: err}
				}
				return &exitErr{code: ExitExternal, err: err}
			}
			fmt.Fprintln(s.deps.Stdout, ws.Path)
			return nil
		},
	}
	// --print is accepted for shell-wrapper forward compatibility; printing
	// the path on stdout is the default and only behaviour today.
	c.Flags().Bool("print", false, "always print the path on stdout (currently the default)")
	return c
}

// runPicker handles the no-name code path for `arat go`: pick a workspace
// and print its path on stdout. Cancel = success with no output.
func (s *state) runPicker(cmd *cobra.Command, svc Service) error {
	chosen, err := s.pickWorkspaceInteractive(cmd, svc)
	if err != nil {
		return err
	}
	if chosen == nil {
		return nil // user cancelled
	}
	fmt.Fprintln(s.deps.Stdout, chosen.Path)
	return nil
}

// pickWorkspaceInteractive loads the shallow workspace list, sorts it by
// recency, and delegates to the injected picker (fzf when available, with a
// bubbletea fallback). Returns nil when the user cancels. Shared between
// `arat go` and `arat rm` so they behave identically in picker mode — same
// sort, same "no workspaces yet" error, same cancel-as-success semantics.
//
// ListShallow skips per-repo git inspection so the picker appears in well
// under a second even when the user has many workspaces with many worktrees.
// The trade-off is that the picker can't show dirty / unpushed / stash
// counts — `arat ls` is the place for those.
func (s *state) pickWorkspaceInteractive(cmd *cobra.Command, svc Service) (*workspace.Workspace, error) {
	if s.deps.PickWorkspace == nil {
		return nil, &exitErr{code: ExitUsage, err: errors.New("interactive picker not available (no PickWorkspace impl wired)")}
	}
	items, err := svc.ListShallow(cmd.Context())
	if err != nil {
		if errors.Is(err, workspace.ErrNoWorkspacesDir) {
			return nil, &exitErr{code: ExitNotFound, err: errors.New("no workspaces yet")}
		}
		return nil, &exitErr{code: ExitExternal, err: err}
	}
	if len(items) == 0 {
		return nil, &exitErr{code: ExitNotFound, err: errors.New("no workspaces yet")}
	}
	// Most-recently-touched first, name asc as tiebreak. mtime on the
	// workspace dir is a cheap proxy for "last visited" — it bumps on most
	// activity (new file at the top level, branch checkout, etc.) without us
	// having to keep a separate visit log.
	sort.SliceStable(items, func(i, j int) bool {
		if !items[i].Created.Equal(items[j].Created) {
			return items[i].Created.After(items[j].Created)
		}
		return items[i].Name < items[j].Name
	})
	chosen, err := s.deps.PickWorkspace(cmd.Context(), items, s.deps.Stderr)
	if err != nil {
		return nil, &exitErr{code: ExitExternal, err: err}
	}
	return chosen, nil
}
