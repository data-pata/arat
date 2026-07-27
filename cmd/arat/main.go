// arat: per-task git-worktree workspaces with Claude context.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/data-pata/arat/internal/cmd"
	"github.com/data-pata/arat/internal/config"
	"github.com/data-pata/arat/internal/git"
	"github.com/data-pata/arat/internal/linear"
	"github.com/data-pata/arat/internal/tui"
	"github.com/data-pata/arat/internal/workspace"
	"golang.org/x/term"
)

// confirm reads a y/N answer from stdin. Empty input or anything other than
// "y" / "yes" (case-insensitive) returns false — destructive-by-Enter would
// defeat the whole point. EOF is treated as a no, not an error.
func confirm(prompt string) (bool, error) {
	fmt.Fprint(os.Stderr, prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

// defaultClaudeProjectsDir returns the location of Claude Code's per-cwd
// session-history root. Honours $CLAUDE_CONFIG_DIR (the supported override),
// falls back to ~/.claude. Returns "" when no home is known, which makes
// the workspace service treat session migration as a no-op rather than fail.
func defaultClaudeProjectsDir() string {
	if v := os.Getenv("CLAUDE_CONFIG_DIR"); v != "" {
		return filepath.Join(v, "projects")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "projects")
}

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
				ClaudeProjectsDir:     defaultClaudeProjectsDir(),
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
		PickContainer: func(ctx context.Context, containers []linear.Container, out io.Writer) (*linear.Container, error) {
			return tui.PickContainer(ctx, containers, out)
		},
		NewLinear: func() cmd.LinearClient { return linear.New() },
		Cwd:       os.Getwd,
		IsTTY:     func() bool { return term.IsTerminal(int(os.Stdin.Fd())) },
		Confirm:   confirm,
		TicketFlow: func(ctx context.Context, lr linear.Reader, opts cmd.TicketFlowOptions, out io.Writer) (cmd.TicketFlowResult, error) {
			res, err := tui.PickTicketFlow(ctx, lr, tui.TicketFlowOptions{Team: opts.Team, AllowSkip: opts.AllowSkip}, out)
			if err != nil {
				return cmd.TicketFlowResult{}, err
			}
			var out_ cmd.TicketFlowResult
			switch res.Action {
			case tui.ActionPick:
				out_.Ticket = res.IssueID
				out_.TicketTitle = res.IssueTitle
			case tui.ActionSkip:
				out_.Skip = true
			case tui.ActionCancelled:
				out_.Cancelled = true
			case tui.ActionCreate:
				out_.NewTitle = res.NewTitle
				out_.NewDescription = res.NewDescription
			}
			return out_, nil
		},
		NameFlow: func(ctx context.Context, def, ticket string, out io.Writer) (cmd.NameFlowResult, error) {
			name, cancelled, err := tui.AskName(ctx, def, ticket, out)
			if err != nil {
				return cmd.NameFlowResult{}, err
			}
			return cmd.NameFlowResult{Cancelled: cancelled, Name: name}, nil
		},
		RepoFlow: func(ctx context.Context, cands []workspace.RepoCandidate, out io.Writer) (cmd.RepoFlowResult, error) {
			selected, cancelled, err := tui.PickRepos(ctx, cands, out)
			if err != nil {
				return cmd.RepoFlowResult{}, err
			}
			return cmd.RepoFlowResult{Cancelled: cancelled, Repos: selected}, nil
		},
	}
	os.Exit(cmd.Execute(deps, os.Args[1:]))
}
