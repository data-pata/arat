package cmd

import (
	"errors"
	"fmt"

	"github.com/data-pata/arat/internal/workspace"
	"github.com/spf13/cobra"
)

func newRmCmd(s *state) *cobra.Command {
	var (
		force        bool
		keepBranches bool
	)

	c := &cobra.Command{
		Use:     "rm <name>",
		Aliases: []string{"kill"},
		Short:   "Remove a workspace and its worktrees",
		Long: `Remove a workspace, its git worktrees, and (by default) the feature
branches in each canonical repo.

By default, refuses if any worktree has uncommitted changes, unpushed commits,
or stash entries — pass --force to override. Use --keep-branches to remove the
worktrees but keep the branches in the canonical repos.

Returns exit code 4 (precondition) when safety checks trip without --force.
`,
		Example: `  arat rm abc-123--postal-fix
  arat rm postal-fix --force
  arat rm postal-fix --keep-branches`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := s.loadConfig()
			if err != nil {
				return err
			}
			svc := s.deps.NewService(cfg)

			err = svc.Remove(cmd.Context(), workspace.RemoveOptions{
				Name:         args[0],
				Force:        force,
				KeepBranches: keepBranches,
			})
			if err != nil {
				return mapRmError(err)
			}

			fmt.Fprintf(s.deps.Stderr, "removed workspace %s\n", args[0])
			return nil
		},
	}
	c.Flags().BoolVarP(&force, "force", "f", false, "remove even if dirty / unpushed / stashed")
	c.Flags().BoolVar(&keepBranches, "keep-branches", false, "do not delete the branches when removing worktrees")
	return c
}

func mapRmError(err error) error {
	var pre *workspace.ErrPrecondition
	switch {
	case errors.Is(err, workspace.ErrNotFound):
		return &exitErr{code: ExitNotFound, err: err}
	case errors.As(err, &pre):
		hint := err.Error() + "\nrun with --force to override"
		return &exitErr{code: ExitPrecondition, err: errors.New(hint)}
	}
	return &exitErr{code: ExitExternal, err: err}
}
