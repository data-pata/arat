// Package cmd wires arat's cobra subcommands.
package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/data-pata/arat/internal/config"
	"github.com/data-pata/arat/internal/linear"
	"github.com/data-pata/arat/internal/output"
	"github.com/data-pata/arat/internal/workspace"
	"github.com/spf13/cobra"
)

// Exit codes (documented in `arat --help` and per-command help).
const (
	ExitOK           = 0
	ExitGeneric      = 1
	ExitUsage        = 2
	ExitNotFound     = 3
	ExitPrecondition = 4
	ExitConflict     = 5
	ExitExternal     = 6
	ExitConfig       = 7
)

// Deps is the set of injected dependencies a command may need.
// Real wiring happens in cmd/arat/main.go; tests inject fakes.
type Deps struct {
	Stdout        io.Writer
	Stderr        io.Writer
	NewConfig     func(path string) (*config.Config, error)
	NewService    func(cfg *config.Config) Service
	PickWorkspace func(ctx context.Context, items []workspace.Workspace, out io.Writer) (*workspace.Workspace, error)
	NewLinear     func() LinearClient
	Cwd           func() (string, error)
	TicketFlow    TicketFlow
	IsTTY         func() bool // returns whether stdin is a tty; defaults to false
}

// LinearClient is the surface Linear-driven commands need. Implemented by
// internal/linear; tests inject a fake.
type LinearClient interface {
	Available(ctx context.Context) error
	IssueList(ctx context.Context, opts linear.IssueListOptions) ([]linear.Issue, error)
	IssueCreate(ctx context.Context, opts linear.IssueCreateOptions) (linear.IssueResult, error)
	CommentAdd(ctx context.Context, opts linear.CommentAddOptions) error
}

// TicketFlow is the interactive ticket-attachment flow. Returns either a
// chosen ticket id (string) or empty (skip). Tests inject a fake; the real
// impl lives in internal/tui.
type TicketFlow func(ctx context.Context, lc linear.Reader, team string, out io.Writer) (TicketFlowResult, error)

// TicketFlowResult: a parallel of tui.TicketFlowResult, but lifted to the
// cmd package so cmd code doesn't import tui directly.
type TicketFlowResult struct {
	Cancelled bool
	Skip      bool
	Ticket    string // when non-empty, attach this ticket
	Hint      string // when non-empty, print this to stderr (and skip)
}

// Service is the workspace-domain surface the commands need.
type Service interface {
	List(ctx context.Context) ([]workspace.Workspace, error)
	Get(ctx context.Context, name string) (*workspace.Workspace, error)
	New(ctx context.Context, opts workspace.NewOptions) (*workspace.Workspace, error)
	Remove(ctx context.Context, opts workspace.RemoveOptions) error
	AttachTicket(ctx context.Context, opts workspace.AttachOptions) (*workspace.AttachResult, error)
	AddRepos(ctx context.Context, opts workspace.AddReposOptions) (*workspace.AddReposResult, error)
}

// Root builds the root cobra command.
func Root(d Deps) *cobra.Command {
	var (
		configPath string
		jsonOut    bool
	)

	root := &cobra.Command{
		Use:   "arat",
		Short: "Per-task git-worktree workspaces with Claude context",
		Long: `arat manages per-task workspaces under a configured directory.
Each workspace is a directory holding git worktrees of one or more repos plus
a CLAUDE.md and claude_workspace/ scratch dir.

Commands accept --json where structured output is useful (ls, go --print).
Stderr is for operational messages; stdout is for results / JSON.

Exit codes:
  0  ok
  1  generic failure (uncategorized)
  2  usage error
  3  not found
  4  precondition failed (dirty / unpushed / stash)
  5  conflict (already exists)
  6  external tool error (git, linear)
  7  config error
`,
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	root.PersistentFlags().StringVar(&configPath, "config", "", "path to config file (default: $ARAT_CONFIG, $XDG_CONFIG_HOME/arat/config.toml, $HOME/.config/arat/config.toml)")
	root.PersistentFlags().BoolVar(&jsonOut, "json", false, "emit JSON output where supported")

	state := &state{
		deps:       d,
		configPath: &configPath,
		jsonOut:    &jsonOut,
	}

	root.AddCommand(
		newLsCmd(state),
		newNewCmd(state),
		newRmCmd(state),
		newGoCmd(state),
		newInitCmd(state),
		newTicketCmd(state),
		newNoteCmd(state),
		newRepoCmd(state),
		newConfigCmd(state),
		newVersionCmd(state),
	)
	return root
}

type state struct {
	deps       Deps
	configPath *string
	jsonOut    *bool
}

func (s *state) writer() *output.Writer {
	w := &output.Writer{Out: s.deps.Stdout, Err: s.deps.Stderr, Format: output.Text}
	if *s.jsonOut {
		w.Format = output.JSON
	}
	return w
}

// loadConfig resolves and loads the config, mapping ErrNotFound to a clear message.
func (s *state) loadConfig() (*config.Config, error) {
	path, err := config.ResolvePath(*s.configPath)
	if err != nil {
		return nil, &exitErr{code: ExitConfig, err: err}
	}
	cfg, err := s.deps.NewConfig(path)
	if err != nil {
		if errors.Is(err, config.ErrNotFound) {
			return nil, &exitErr{
				code: ExitConfig,
				err:  fmt.Errorf("%w\nrun `arat config init` to create one", err),
			}
		}
		return nil, &exitErr{code: ExitConfig, err: err}
	}
	return cfg, nil
}

// exitErr wraps an error with the exit code arat should return.
type exitErr struct {
	code int
	err  error
}

func (e *exitErr) Error() string { return e.err.Error() }
func (e *exitErr) Unwrap() error { return e.err }

// Execute runs the root command and returns the desired process exit code.
func Execute(d Deps, args []string) int {
	if d.Stdout == nil {
		d.Stdout = os.Stdout
	}
	if d.Stderr == nil {
		d.Stderr = os.Stderr
	}
	cmd := Root(d)
	cmd.SetArgs(args)
	cmd.SetOut(d.Stdout)
	cmd.SetErr(d.Stderr)
	err := cmd.Execute()
	if err == nil {
		return ExitOK
	}
	fmt.Fprintf(d.Stderr, "error: %v\n", err)
	var ee *exitErr
	if errors.As(err, &ee) {
		return ee.code
	}
	return ExitGeneric
}
