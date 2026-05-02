// arat: per-task git-worktree workspaces with Claude context.
package main

import (
	"context"
	"fmt"
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
			svc, err := workspace.NewService(workspace.ServiceOptions{
				Root:                  cfg.Root,
				WorkspacesDir:         cfg.WorkspacesDir,
				BranchPrefix:          cfg.BranchPrefix,
				TicketRE:              cfg.TicketRegex(),
				TicketURL:             cfg.TicketURL,
				DefaultRepos:          cfg.DefaultRepos,
				AutoReposGlob:         cfg.AutoReposGlob,
				GenerateCodeWorkspace: cfg.GenerateCodeWorkspace,
				Git:                   git.New(),
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "arat: %v\n", err)
				os.Exit(cmd.ExitConfig)
			}
			return svc
		},
		PickWorkspace: func(ctx context.Context, items []workspace.Workspace, out io.Writer) (*workspace.Workspace, error) {
			ws, err := tui.PickWorkspace(ctx, items, out)
			if err != nil {
				return nil, err
			}
			return ws, nil
		},
		NewLinear: func() cmd.LinearClient { return linear.New() },
		Cwd:       os.Getwd,
		IsTTY:     func() bool { return term.IsTerminal(int(os.Stdin.Fd())) },
		TicketFlow: func(ctx context.Context, lr linear.Reader, team string, out io.Writer) (cmd.TicketFlowResult, error) {
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
