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
		recursive    bool
	)

	c := &cobra.Command{
		Use:     "rm [name]",
		Aliases: []string{"kill"},
		Short:   "Remove a workspace and its worktrees",
		Long: `Remove a workspace, its git worktrees, and (by default) the feature
branches in each canonical repo.

Takes a ref: either the full path from workspaces_dir
("q3-billing/abc-12--invoice") or a bare name that is unique across the tree.
Without one, opens an interactive picker (fzf when available, with a
bubbletea fallback) sorted by recency — same picker as "arat go".

By default, refuses if any worktree has uncommitted changes or unpushed commits
— pass --force to override. Use --keep-branches to remove the worktrees but
keep the branches in the canonical repos.

Removing a project takes everything nested inside it with it, so that requires
--recursive. --force does not stand in for --recursive: --force is about
discarding changes, --recursive is about discarding whole workspaces. With
--recursive, the worktrees of every nested workspace are checked and cleaned
up too.

Stash entries do not block: stash refs live on the canonical clone and survive
worktree removal, so removal proceeds and a note is printed pointing at the
canonical clone where the stashes can still be recovered.

Returns exit code 4 (precondition) when safety checks trip without --force, or
when a project still has nested workspaces and --recursive was not given.
`,
		Example: `  arat rm                       # interactive picker
  arat rm abc-123--postal-fix
  arat rm postal-fix --force
  arat rm postal-fix --keep-branches
  arat rm q3-billing/abc-12--invoice
  arat rm q3-billing --recursive`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := s.loadConfig()
			if err != nil {
				return err
			}
			svc := s.deps.NewService(cfg)

			var name string
			if len(args) == 1 {
				name = args[0]
			} else {
				chosen, err := s.pickWorkspaceInteractive(cmd, svc)
				if err != nil {
					return err
				}
				if chosen == nil {
					return nil // user cancelled the picker
				}
				// Picker-mode confirmation: a stray Enter on the picker
				// would otherwise silently nuke whichever workspace was
				// highlighted. Explicit-name `arat rm foo` skips this —
				// typing the full name is itself a confirmation.
				if s.deps.Confirm == nil {
					return &exitErr{code: ExitUsage, err: errors.New("interactive confirm not available (no Confirm impl wired)")}
				}
				ok, err := s.deps.Confirm(rmPrompt(*chosen))
				if err != nil {
					return &exitErr{code: ExitExternal, err: err}
				}
				if !ok {
					fmt.Fprintln(s.deps.Stderr, "cancelled")
					return nil
				}
				name = chosen.Ref
			}

			res, err := svc.Remove(cmd.Context(), workspace.RemoveOptions{
				Name:         name,
				Force:        force,
				KeepBranches: keepBranches,
				Recursive:    recursive,
			})
			if err != nil {
				return mapRmError(err)
			}

			fmt.Fprintf(s.deps.Stderr, "removed workspace %s\n", name)
			if res != nil {
				for _, sr := range res.StashedRepos {
					fmt.Fprintf(s.deps.Stderr,
						"  note: %d stash %s preserved in %s (git -C %s stash list)\n",
						sr.Stashes, pluralize(sr.Stashes, "entry", "entries"), sr.CanonicalRepo, sr.CanonicalRepo)
				}
			}
			return nil
		},
	}
	c.Flags().BoolVarP(&force, "force", "f", false, "remove even if dirty or unpushed")
	c.Flags().BoolVar(&keepBranches, "keep-branches", false, "do not delete the branches when removing worktrees")
	c.Flags().BoolVar(&recursive, "recursive", false, "also remove the workspaces nested inside a project")
	return c
}

// rmPrompt is the picker-mode confirmation. For a project it spells out how
// many workspaces go with it, because the directory name alone does not
// convey that removing it removes everything underneath.
func rmPrompt(ws workspace.Workspace) string {
	if n := len(workspace.Descendants(ws)); n > 0 {
		return fmt.Sprintf("Remove project %q and the %d %s nested in it? [y/N]: ",
			ws.Ref, n, pluralize(n, "workspace", "workspaces"))
	}
	return fmt.Sprintf("Remove workspace %q? [y/N]: ", ws.Ref)
}

func pluralize(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func mapRmError(err error) error {
	var pre *workspace.ErrPrecondition
	var notEmpty *workspace.ErrNotEmpty
	var ambiguous *workspace.ErrAmbiguous
	switch {
	case errors.Is(err, workspace.ErrNotFound):
		return &exitErr{code: ExitNotFound, err: err}
	case errors.As(err, &ambiguous):
		return &exitErr{code: ExitUsage, err: err}
	case errors.As(err, &notEmpty):
		return &exitErr{code: ExitPrecondition, err: fmt.Errorf("%w\nrun with --recursive to remove them too", err)}
	case errors.As(err, &pre):
		return &exitErr{code: ExitPrecondition, err: fmt.Errorf("%w\nrun with --force to override", err)}
	}
	return &exitErr{code: ExitExternal, err: err}
}
