// arat: per-task git-worktree workspaces with Claude context.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

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

// gitClient and linearClient build the subprocess wrappers from config.
// ARAT_TRACE (any non-empty value) additionally logs every subprocess to
// stderr with its argv and duration — everything arat does is a subprocess,
// so this is the observability switch for "what ran, and what did it cost".
func gitClient(cfg *config.Config) *git.Git {
	if os.Getenv("ARAT_TRACE") != "" {
		return git.NewTraced(cfg.CommandTimeoutDuration(), os.Stderr)
	}
	return git.NewWithTimeout(cfg.CommandTimeoutDuration())
}

func linearClient(cfg *config.Config) *linear.Linear {
	if os.Getenv("ARAT_TRACE") != "" {
		return linear.NewTraced(cfg.CommandTimeoutDuration(), os.Stderr)
	}
	return linear.NewWithTimeout(cfg.CommandTimeoutDuration())
}

func main() {
	// Ctrl-C / SIGTERM cancel the root context, which reaches every git and
	// linear subprocess (and `new`'s errgroup) through cmd.Context(), so
	// in-flight work stops and failure cleanup still runs. After the first
	// signal, restore the default disposition: a second Ctrl-C must kill the
	// process even if cleanup hangs.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop()
	}()

	deps := cmd.Deps{
		Stdout:    os.Stdout,
		Stderr:    os.Stderr,
		NewConfig: config.Load,
		NewService: func(cfg *config.Config) (cmd.Service, error) {
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
				Git:                   gitClient(cfg),
			})
			if err != nil {
				return nil, err
			}
			return svc, nil
		},
		PickWorkspace: tui.PickWorkspace,
		PickContainer: func(ctx context.Context, containers []linear.Container, out io.Writer) (*linear.Container, error) {
			return tui.PickContainer(ctx, containers, out)
		},
		NewLinear: func(cfg *config.Config) cmd.LinearClient { return linearClient(cfg) },
		Cwd:       os.Getwd,
		// Interactive needs both ends of the conversation: the pickers read
		// keys from stdin (or /dev/tty) and render to stderr. Probing stdin
		// alone would let `arat go 2>log` write escape sequences into the
		// log instead of showing a picker.
		IsTTY: func() bool {
			return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stderr.Fd()))
		},
		Confirm:    confirm,
		TicketFlow: tui.PickTicketFlow,
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
	os.Exit(int(cmd.Execute(ctx, deps, os.Args[1:])))
}
