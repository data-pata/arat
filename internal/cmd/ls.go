package cmd

import (
	"errors"
	"fmt"
	"io"

	"github.com/data-pata/arat/internal/workspace"
	"github.com/spf13/cobra"
)

func newLsCmd(s *state) *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List workspaces with status markers",
		Long: `List all workspaces under the configured workspaces_dir.

For each workspace, prints each repo's branch and any of:
  *dirty*     working tree has uncommitted changes
  *unpushed*  commits ahead of upstream
  *stashes:N* N stash entries

With --json, emits an array of workspace objects (path, ticket, repos[], etc).
`,
		Example: "  arat ls\n  arat ls --json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := s.loadConfig()
			if err != nil {
				return err
			}
			svc := s.deps.NewService(cfg)
			items, err := svc.List(cmd.Context())
			if err != nil {
				if errors.Is(err, workspace.ErrNoWorkspacesDir) {
					// No workspaces yet is not an error; print an informational note.
					s.writer().JSONRecord([]workspace.Workspace{}, func(out io.Writer) {
						fmt.Fprintf(s.deps.Stderr, "no workspaces yet (workspaces_dir does not exist: %s)\n", cfg.WorkspacesDir)
					})
					return nil
				}
				return &exitErr{code: ExitExternal, err: err}
			}
			s.writer().JSONRecord(items, func(out io.Writer) { writeLsText(out, items) })
			return nil
		},
	}
}

func writeLsText(out io.Writer, items []workspace.Workspace) {
	if len(items) == 0 {
		fmt.Fprintln(out, "no workspaces")
		return
	}
	for i, ws := range items {
		if i > 0 {
			fmt.Fprintln(out)
		}
		fmt.Fprintf(out, "── %s ──\n", ws.Name)
		if ws.TicketURL != "" {
			fmt.Fprintf(out, "  %s\n", ws.TicketURL)
		}
		for _, r := range ws.Repos {
			branch := r.Branch
			if branch == "" {
				branch = "?"
			}
			line := fmt.Sprintf("  %s → %s", r.Name, branch)
			if r.Dirty {
				line += " *dirty*"
			}
			if r.Unpushed {
				line += " *unpushed*"
			}
			if r.Stashes > 0 {
				line += fmt.Sprintf(" *stashes:%d*", r.Stashes)
			}
			fmt.Fprintln(out, line)
		}
		if len(ws.Repos) == 0 {
			fmt.Fprintln(out, "  (no worktrees)")
		}
	}
}
