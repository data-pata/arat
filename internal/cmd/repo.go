package cmd

import (
	"errors"
	"fmt"

	"github.com/data-pata/arat/internal/workspace"
	"github.com/spf13/cobra"
)

func newRepoCmd(s *state) *cobra.Command {
	c := &cobra.Command{
		Use:   "repo",
		Short: "Manage repos within a workspace",
	}
	c.AddCommand(newRepoAddCmd(s))
	return c
}

func newRepoAddCmd(s *state) *cobra.Command {
	var (
		wsName    string
		base      string
		recursive bool
	)
	c := &cobra.Command{
		Use:   "add <repo>...",
		Short: "Add one or more git worktrees to an existing workspace",
		Long: `Add one or more git worktrees to an existing multi-repo workspace.

Each <repo> must exist as a canonical clone at <root>/<repo>. A new worktree
is created at <workspace>/<repo>, with a fresh branch matching the workspace's
existing feature branch and branched off origin/HEAD (or --base).

If --workspace is omitted, the workspace is inferred from the current
directory. Refuses if the workspace is a single-repo layout (the workspace
dir itself is a worktree).

With --recursive, the repos are also added to every workspace nested under
the target, each on its own feature branch. Workspaces that already carry a
repo are skipped rather than errors, so it is safe to run over a tree where
some members already have it. Every workspace branches off the same base —
fan-out never stacks children on their parent's branch.

Regenerates <name>.code-workspace if one already exists. Does not edit
CLAUDE.md — its **Repos**: line may be stale until you re-render it.
`,
		Example: `  arat repo add ui-app
  arat repo add core-mono ui-app --workspace abc-123--postal-fix
  arat repo add ui-app --base origin/main
  arat repo add ui-app --workspace q3-billing --recursive`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := s.loadConfig()
			if err != nil {
				return err
			}
			svc := s.deps.NewService(cfg)
			name := wsName
			if name == "" {
				if s.deps.Cwd == nil {
					return &exitErr{code: ExitUsage, err: errors.New("--workspace not given and cwd resolver not configured")}
				}
				wd, err := s.deps.Cwd()
				if err != nil {
					return &exitErr{code: ExitUsage, err: err}
				}
				resolved, err := svc.WorkspaceAt(cmd.Context(), wd)
				if err != nil {
					return &exitErr{code: ExitUsage, err: err}
				}
				name = resolved.Ref
			}
			res, err := svc.AddRepos(cmd.Context(), workspace.AddReposOptions{
				Workspace: name,
				Repos:     args,
				Base:      base,
				Recursive: recursive,
			})
			if err != nil {
				return mapAddReposError(err)
			}

			for _, o := range res.Outcomes {
				if len(o.Added) > 0 {
					fmt.Fprintf(s.deps.Stderr, "added %d repo(s) to %s\n", len(o.Added), o.Ref)
					for _, r := range o.Added {
						fmt.Fprintf(s.deps.Stderr, "  %s → %s\n", r.Name, r.Branch)
						fmt.Fprintf(s.deps.Stdout, "%s\n", r.Path)
					}
				}
				for _, reason := range o.Skipped {
					fmt.Fprintf(s.deps.Stderr, "skipped %s: %s\n", o.Ref, reason)
				}
			}
			return nil
		},
	}
	c.Flags().StringVar(&wsName, "workspace", "", "workspace name (default: inferred from cwd)")
	c.Flags().StringVar(&base, "base", "", "branch base (default: origin/HEAD)")
	c.Flags().BoolVar(&recursive, "recursive", false, "also add the repos to every workspace nested under the target")
	return c
}

func mapAddReposError(err error) error {
	var pre *workspace.ErrPrecondition
	switch {
	case errors.Is(err, workspace.ErrNotFound):
		return &exitErr{code: ExitNotFound, err: err}
	case errors.Is(err, workspace.ErrAlreadyExists):
		return &exitErr{code: ExitConflict, err: err}
	case errors.As(err, &pre):
		return &exitErr{code: ExitPrecondition, err: err}
	}
	return &exitErr{code: ExitExternal, err: err}
}
