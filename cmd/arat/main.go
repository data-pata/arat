// arat: per-task git-worktree workspaces with Claude context.
package main

import (
	"context"
	"io"
	"os"

	"github.com/data-pata/arat/internal/cmd"
	"github.com/data-pata/arat/internal/config"
	"github.com/data-pata/arat/internal/git"
	"github.com/data-pata/arat/internal/linear"
	"github.com/data-pata/arat/internal/tui"
	"github.com/data-pata/arat/internal/workspace"
	"golang.org/x/term"
)

func main() {
	deps := cmd.Deps{
		Stdout:    os.Stdout,
		Stderr:    os.Stderr,
		NewConfig: config.Load,
		NewService: func(cfg *config.Config) cmd.Service {
			return &workspace.Service{
				Root:                  cfg.Root,
				WorkspacesDir:         cfg.WorkspacesDir,
				BranchPrefix:          cfg.BranchPrefix,
				TicketRE:              cfg.TicketRegex(),
				TicketURL:             cfg.TicketURL,
				DefaultRepos:          cfg.DefaultRepos,
				AutoReposGlob:         cfg.AutoReposGlob,
				GenerateCodeWorkspace: cfg.GenerateCodeWorkspace,
				Git:                   gitAdapter{g: git.New()},
			}
		},
		PickWorkspace: func(ctx context.Context, items []cmd.Workspace, out io.Writer) (*cmd.Workspace, error) {
			ws, err := tui.PickWorkspace(ctx, items, out)
			if err != nil {
				return nil, err
			}
			return ws, nil
		},
		NewLinear: func() cmd.LinearClient { return linear.New() },
		Cwd:       os.Getwd,
		IsTTY:     func() bool { return term.IsTerminal(int(os.Stdin.Fd())) },
		TicketFlow: func(ctx context.Context, lr cmd.LinearReader, team string, out io.Writer) (cmd.TicketFlowResult, error) {
			res, err := tui.PickTicketFlow(ctx, lr, team, out)
			if err != nil {
				return cmd.TicketFlowResult{}, err
			}
			out_ := cmd.TicketFlowResult{Hint: res.HintText}
			switch res.Action {
			case tui.ActionPick:
				out_.Ticket = res.IssueID
			case tui.ActionSkip:
				out_.Skip = true
			case tui.ActionCancelled:
				out_.Cancelled = true
			case tui.ActionCreate:
				out_.Skip = true // create-new path: print hint, don't fail
			}
			return out_, nil
		},
	}
	os.Exit(cmd.Execute(deps, os.Args[1:]))
}

// gitAdapter bridges *git.Git to workspace.Git. The workspace package declares
// its own Inspection type so it doesn't import internal/git.
type gitAdapter struct{ g *git.Git }

func (a gitAdapter) IsWorktree(ctx context.Context, dir string) bool {
	return a.g.IsWorktree(ctx, dir)
}

func (a gitAdapter) CanonicalRepoName(ctx context.Context, dir string) string {
	return a.g.CanonicalRepoName(ctx, dir)
}

func (a gitAdapter) CanonicalRepoPath(ctx context.Context, dir string) string {
	return a.g.CanonicalRepoPath(ctx, dir)
}

func (a gitAdapter) Inspect(ctx context.Context, dir string) (workspace.Inspection, error) {
	in, err := a.g.Inspect(ctx, dir)
	if err != nil {
		return workspace.Inspection{}, err
	}
	return workspace.Inspection{
		Branch:   in.Branch,
		Dirty:    in.Dirty,
		Unpushed: in.Unpushed,
		Stashes:  in.Stashes,
	}, nil
}

func (a gitAdapter) Fetch(ctx context.Context, repoDir string) error {
	return a.g.Fetch(ctx, repoDir)
}

func (a gitAdapter) WorktreeAdd(ctx context.Context, repoDir, branch, target, base string) error {
	return a.g.WorktreeAdd(ctx, repoDir, branch, target, base)
}

func (a gitAdapter) WorktreeRemove(ctx context.Context, repoDir, target string, force bool) error {
	return a.g.WorktreeRemove(ctx, repoDir, target, force)
}

func (a gitAdapter) BranchDelete(ctx context.Context, repoDir, branch string, force bool) error {
	return a.g.BranchDelete(ctx, repoDir, branch, force)
}

func (a gitAdapter) BranchRename(ctx context.Context, repoDir, from, to string) error {
	return a.g.BranchRename(ctx, repoDir, from, to)
}

func (a gitAdapter) WorktreeRepair(ctx context.Context, repoDir string) error {
	return a.g.WorktreeRepair(ctx, repoDir)
}
