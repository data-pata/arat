package cmd

import (
	"errors"
	"fmt"

	"github.com/data-pata/arat/internal/workspace"
	"github.com/spf13/cobra"
)

func newGoCmd(s *state) *cobra.Command {
	var print bool

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
	c.Flags().BoolVar(&print, "print", false, "always print the path on stdout (currently the default)")
	return c
}

// runPicker handles the no-name code path: list workspaces and let the user
// pick one via the injected picker. If the user cancels, no path is printed
// and the command exits successfully.
func (s *state) runPicker(cmd *cobra.Command, svc Service) error {
	if s.deps.PickWorkspace == nil {
		return &exitErr{code: ExitUsage, err: errors.New("interactive picker not available (no PickWorkspace impl wired)")}
	}
	items, err := svc.List(cmd.Context())
	if err != nil {
		if errors.Is(err, workspace.ErrNoWorkspacesDir) {
			return &exitErr{code: ExitNotFound, err: errors.New("no workspaces yet")}
		}
		return &exitErr{code: ExitExternal, err: err}
	}
	if len(items) == 0 {
		return &exitErr{code: ExitNotFound, err: errors.New("no workspaces yet")}
	}
	chosen, err := s.deps.PickWorkspace(cmd.Context(), items, s.deps.Stderr)
	if err != nil {
		return &exitErr{code: ExitExternal, err: err}
	}
	if chosen == nil {
		return nil // user cancelled
	}
	fmt.Fprintln(s.deps.Stdout, chosen.Path)
	return nil
}
