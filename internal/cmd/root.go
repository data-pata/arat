// Package cmd wires arat's cobra subcommands.
package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

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
	// PickContainer is the interactive picker over Linear projects and
	// initiatives, used by `arat project link` when no --project/--initiative
	// was given. Returns nil (no error) when the user cancels.
	PickContainer func(ctx context.Context, containers []linear.Container, out io.Writer) (*linear.Container, error)
	NewLinear     func() LinearClient
	Cwd           func() (string, error)
	TicketFlow    TicketFlow
	RepoFlow      RepoFlow
	IsTTY         func() bool                       // returns whether stdin is a tty; defaults to false
	Confirm       func(prompt string) (bool, error) // y/N prompt; returns true only on explicit yes
}

// LinearClient is the surface Linear-driven commands need. Implemented by
// internal/linear; tests inject a fake.
type LinearClient interface {
	Available(ctx context.Context) error
	IssueList(ctx context.Context, opts linear.IssueListOptions) ([]linear.Issue, error)
	IssueCreate(ctx context.Context, opts linear.IssueCreateOptions) (linear.IssueResult, error)
	CommentAdd(ctx context.Context, opts linear.CommentAddOptions) error
	// ContainerList returns Linear projects or initiatives ("project" /
	// "initiative"), the two things `arat project link` can attach.
	ContainerList(ctx context.Context, kind string) ([]linear.Container, error)
}

// TicketFlow is the interactive ticket-attachment flow. Returns either a
// chosen ticket id (string) or empty (skip). Tests inject a fake; the real
// impl lives in internal/tui.
type TicketFlow func(ctx context.Context, lc linear.Reader, team string, out io.Writer) (TicketFlowResult, error)

// TicketFlowResult: a parallel of tui.TicketFlowResult, but lifted to the
// cmd package so cmd code doesn't import tui directly.
type TicketFlowResult struct {
	Cancelled      bool
	Skip           bool
	Ticket         string // when non-empty, attach this existing ticket
	NewTitle       string // when non-empty, cmd creates a new ticket with this title
	NewDescription string // optional description paired with NewTitle
}

// RepoFlow is the interactive multi-select repo picker. Returns either the
// chosen repo names (Repos) or Cancelled. The cmd layer is responsible for
// fetching the candidate list from the workspace service before invoking it.
type RepoFlow func(ctx context.Context, candidates []workspace.RepoCandidate, out io.Writer) (RepoFlowResult, error)

// RepoFlowResult is the cmd-side outcome of RepoFlow.
type RepoFlowResult struct {
	Cancelled bool
	Repos     []string // non-empty when the user confirmed a selection
}

// Service is the workspace-domain surface the commands need.
type Service interface {
	List(ctx context.Context) ([]workspace.Workspace, error)
	// ListLight is List with repo names and branches only, read from the
	// filesystem — no git subprocesses, no dirty/unpushed/stash state.
	ListLight(ctx context.Context) ([]workspace.Workspace, error)
	ListShallow(ctx context.Context) ([]workspace.Workspace, error)
	Get(ctx context.Context, ref string) (*workspace.Workspace, error)
	New(ctx context.Context, opts workspace.NewOptions) (*workspace.Workspace, error)
	Remove(ctx context.Context, opts workspace.RemoveOptions) (*workspace.RemoveResult, error)
	AttachTicket(ctx context.Context, opts workspace.AttachOptions) (*workspace.AttachResult, error)
	AddRepos(ctx context.Context, opts workspace.AddReposOptions) (*workspace.AddReposResult, error)
	ListRepoCandidates() ([]workspace.RepoCandidate, error)
	MoveSessionFile(ctx context.Context, sessionID, targetWorkspacePath string) (srcPath, dstPath string, err error)
	// WorkspaceAt resolves the workspace containing a directory (deepest wins).
	WorkspaceAt(ctx context.Context, dir string) (*workspace.Workspace, error)
	// ProjectAt resolves the nearest project containing a directory, or
	// (nil, nil) when the directory is not inside a project.
	ProjectAt(ctx context.Context, dir string) (*workspace.Workspace, error)
	LinkLinear(ctx context.Context, opts workspace.LinkOptions) (*workspace.Workspace, error)
	UnlinkLinear(ctx context.Context, ref string) (*workspace.Workspace, error)
}

// Root builds the root cobra command.
func Root(d Deps) *cobra.Command {
	var (
		configPath string
		jsonOut    bool
		verbose    bool
	)

	root := &cobra.Command{
		Use:   "arat",
		Short: "Per-task git-worktree workspaces with Claude context",
		Long: `arat manages per-task workspaces under a configured directory.
Each workspace is a directory holding git worktrees of one or more repos plus
a CLAUDE.md and claude_workspace/ scratch dir.

Workspaces nest, following Linear's shape. A project workspace ("arat new
<name> --project") contains other workspaces as subdirectories and may itself
carry worktrees on a long-lived branch. Task workspaces nest too: a task
inside a project is that project's issue, a task inside a task is a sub-issue
of it. Projects are always top level, because Linear has no project inside a
project or inside an issue.

Commands that address a single workspace take a ref — the slash-joined path
from workspaces_dir, e.g. "q3-billing/abc-12--invoice" — or a bare name that
is unique across the tree. An ambiguous bare name is an error listing the
candidates; "./<ref>" matches a ref exactly, which is how a top-level
workspace is addressed when a nested one shares its name.

Commands accept --json where structured output is useful (ls, new, go, rm,
repo add, ticket create, project link|unlink). Stderr is for operational
messages; stdout is for results / JSON.

Exit codes:
  0  ok
  1  generic failure (uncategorized)
  2  usage error
  3  not found
  4  precondition failed (dirty / unpushed; non-empty project)
  5  conflict (already exists)
  6  external tool error (git, linear)
  7  config error
`,
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	root.PersistentFlags().StringVar(&configPath, "config", "", "path to config file (default: $ARAT_CONFIG, $XDG_CONFIG_HOME/arat/config.toml, $HOME/.config/arat/config.toml)")
	root.PersistentFlags().BoolVar(&jsonOut, "json", false, "emit JSON output where supported")
	// No backticks in the usage string: pflag renders backticked text as the
	// flag's value placeholder, which would make a boolean flag read as if
	// it took an argument.
	root.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "emit per-step progress to stderr (one line per repo during new)")

	state := &state{
		deps:       d,
		configPath: &configPath,
		jsonOut:    &jsonOut,
		verbose:    &verbose,
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
		newProjectCmd(state),
		newConfigCmd(state),
		newVersionCmd(state),
	)

	// Cobra-detected problems — unknown flags, wrong argument counts — are
	// usage errors and must exit 2 like arat's own validation, not fall
	// through to the generic 1. Flag errors route through FlagErrorFunc;
	// Args validators are wrapped per command. Both carry the command's
	// usage line: "requires at least 1 arg(s)" alone tells the user they
	// are wrong without telling them what right looks like.
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return &exitErr{code: ExitUsage, err: withUsageHint(cmd, err)}
	})
	wrapArgsAsUsageErrors(root)
	return root
}

// wrapArgsAsUsageErrors wraps every command's positional-args validator so a
// failure carries ExitUsage instead of the generic exit code, along with the
// command's usage line.
func wrapArgsAsUsageErrors(c *cobra.Command) {
	if orig := c.Args; orig != nil {
		c.Args = func(cmd *cobra.Command, args []string) error {
			if err := orig(cmd, args); err != nil {
				return &exitErr{code: ExitUsage, err: withUsageHint(cmd, err)}
			}
			return nil
		}
	}
	for _, sub := range c.Commands() {
		wrapArgsAsUsageErrors(sub)
	}
}

// withUsageHint appends the failing command's usage line and a --help pointer
// to a usage error, so the message both names the mistake and shows the shape
// that was expected.
func withUsageHint(cmd *cobra.Command, err error) error {
	return fmt.Errorf("%w\nusage: %s\nsee 'arat %s --help'", err, cmd.UseLine(), commandPathTail(cmd))
}

// commandPathTail is the command's path without the root name ("project
// link"), for composing "arat project link --help".
func commandPathTail(cmd *cobra.Command) string {
	return strings.TrimSpace(strings.TrimPrefix(cmd.CommandPath(), cmd.Root().Name()))
}

type state struct {
	deps       Deps
	configPath *string
	jsonOut    *bool
	verbose    *bool
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

// workspaceFromCwd resolves the workspace containing the current directory,
// for commands whose ref argument is optional because "the workspace I am
// standing in" is the common case.
func workspaceFromCwd(ctx context.Context, svc Service, cwdFn func() (string, error)) (*workspace.Workspace, error) {
	if cwdFn == nil {
		return nil, errors.New("cwd resolver not configured")
	}
	cwd, err := cwdFn()
	if err != nil {
		return nil, err
	}
	return svc.WorkspaceAt(ctx, cwd)
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
	// Unknown subcommands surface as plain errors from cobra with no hook
	// to wrap them (unlike flag and args errors); they are usage errors all
	// the same.
	if strings.HasPrefix(err.Error(), "unknown command") {
		return ExitUsage
	}
	return ExitGeneric
}
