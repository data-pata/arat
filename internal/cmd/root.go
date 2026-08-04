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
	"github.com/data-pata/arat/internal/git"
	"github.com/data-pata/arat/internal/linear"
	"github.com/data-pata/arat/internal/output"
	"github.com/data-pata/arat/internal/tui"
	"github.com/data-pata/arat/internal/workspace"
	"github.com/spf13/cobra"
)

// ExitCode is arat's process exit code. Typed so an exitErr can only carry
// one of the documented codes below, not an arbitrary integer.
type ExitCode int

// Exit codes (documented in `arat --help` and per-command help).
const (
	ExitOK           ExitCode = 0
	ExitGeneric      ExitCode = 1
	ExitUsage        ExitCode = 2
	ExitNotFound     ExitCode = 3
	ExitPrecondition ExitCode = 4
	ExitConflict     ExitCode = 5
	ExitExternal     ExitCode = 6
	ExitConfig       ExitCode = 7
	// ExitInterrupted follows the shell convention of 128+SIGINT for a run
	// cut short by a signal. Cleanup has already run by the time it is
	// returned — cancellation reaches the commands through their context.
	ExitInterrupted ExitCode = 130
)

// Deps is the set of injected dependencies a command may need.
// Real wiring happens in cmd/arat/main.go; tests inject fakes.
type Deps struct {
	Stdout    io.Writer
	Stderr    io.Writer
	NewConfig func(path string) (*config.Config, error)
	// NewService builds the workspace service for a loaded config. A
	// returned error is mapped to the config exit code by state.service —
	// construction can only fail on config-shaped problems (missing root,
	// missing workspaces dir), and returning it keeps process exit out of
	// the wiring layer.
	NewService    func(cfg *config.Config) (Service, error)
	PickWorkspace func(ctx context.Context, items []workspace.Workspace, out io.Writer) (*workspace.Workspace, error)
	// PickContainer is the interactive picker over Linear projects and
	// initiatives, used by `arat attach` on a project workspace (and the
	// legacy `arat project link`) when nothing was named. Returns nil (no
	// error) when the user cancels.
	PickContainer func(ctx context.Context, containers []linear.Container, out io.Writer) (*linear.Container, error)
	// NewLinear builds the Linear client. It takes the loaded config so the
	// wiring can honour per-subprocess settings (command_timeout).
	NewLinear  func(cfg *config.Config) LinearClient
	Cwd        func() (string, error)
	TicketFlow TicketFlow
	RepoFlow   RepoFlow
	NameFlow   NameFlow
	IsTTY      func() bool                       // returns whether stdin is a tty; defaults to false
	Confirm    func(prompt string) (bool, error) // y/N prompt; returns true only on explicit yes
}

// LinearClient is the surface Linear-driven commands need. Implemented by
// internal/linear; tests inject a fake.
type LinearClient interface {
	Available(ctx context.Context) error
	IssueList(ctx context.Context, opts linear.IssueListOptions) ([]linear.Issue, error)
	// IssueTitle fetches one issue's title, for deriving a workspace name
	// from `arat new --ticket <id>` without a name argument.
	IssueTitle(ctx context.Context, id string) (string, error)
	// IssueAssignMe assigns an issue to the viewer, offered after picking an
	// unassigned issue.
	IssueAssignMe(ctx context.Context, id string) error
	IssueCreate(ctx context.Context, opts linear.IssueCreateOptions) (linear.IssueResult, error)
	CommentAdd(ctx context.Context, opts linear.CommentAddOptions) error
	// ContainerList returns Linear projects or initiatives ("project" /
	// "initiative"), the two things a project workspace can attach to.
	ContainerList(ctx context.Context, kind string) ([]linear.Container, error)
	// ProjectCreate creates a Linear project, for `arat attach --new` on a
	// project workspace.
	ProjectCreate(ctx context.Context, opts linear.ProjectCreateOptions) (linear.Container, error)
}

// The helpers below each consume a slice of LinearClient, declared here so
// the function signature says which calls a helper can make — the wiring
// still passes the full client, which satisfies all of them.

// issueCreator is what createTicket needs: probe the binary, create issues.
type issueCreator interface {
	Available(ctx context.Context) error
	IssueCreate(ctx context.Context, opts linear.IssueCreateOptions) (linear.IssueResult, error)
}

// issueTitler is what issue-title derivation needs.
type issueTitler interface {
	Available(ctx context.Context) error
	IssueTitle(ctx context.Context, id string) (string, error)
}

// issueAssigner is what the self-assign offer needs.
type issueAssigner interface {
	IssueAssignMe(ctx context.Context, id string) error
}

// TicketFlow is the interactive ticket-attachment flow. The seam that keeps
// terminals out of tests is the function type — fakes are closures, the real
// impl (tui.PickTicketFlow) is wired in cmd/arat/main.go. The options and
// result are tui's own types on purpose: a hand-mirrored copy would need a
// conversion switch over TicketAction, and a non-exhaustive one silently
// degrades a newly added action into "no ticket".
type TicketFlow func(ctx context.Context, lc linear.Reader, opts tui.TicketFlowOptions, out io.Writer) (tui.TicketFlowResult, error)

// NameFlow is the interactive workspace-name prompt `arat new` opens when no
// name argument was given, pre-filled with a slug derived from the issue
// title. Returns the accepted name ("" when left empty) or Cancelled.
type NameFlow func(ctx context.Context, def, ticket string, out io.Writer) (NameFlowResult, error)

// NameFlowResult is the cmd-side outcome of NameFlow.
type NameFlowResult struct {
	Cancelled bool
	Name      string
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
	// List walks the workspace tree; the Detail option picks the per-repo
	// cost (full git inspection, filesystem-only, or no repos at all).
	List(ctx context.Context, opts workspace.ListOptions) ([]workspace.Workspace, error)
	Get(ctx context.Context, ref string) (*workspace.Workspace, error)
	New(ctx context.Context, opts workspace.NewOptions) (*workspace.Workspace, error)
	Remove(ctx context.Context, opts workspace.RemoveOptions) (*workspace.RemoveResult, error)
	AttachTicket(ctx context.Context, opts workspace.AttachOptions) (*workspace.AttachResult, error)
	AddRepos(ctx context.Context, opts workspace.AddReposOptions) (*workspace.AddReposResult, error)
	ListRepoCandidates() ([]workspace.RepoCandidate, error)
	MoveSessionFile(ctx context.Context, sessionID, targetWorkspacePath string) (srcPath, dstPath string, err error)
	// ForkSessionFile copies a session under a fresh id into the workspace's
	// project dir, leaving the source session untouched.
	ForkSessionFile(ctx context.Context, sessionID, targetWorkspacePath string) (srcPath, dstPath, newID string, err error)
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
attach, detach, repo add, ticket create). Stderr is for operational
messages; stdout is for results / JSON.

Exit codes:
  0  ok
  1  generic failure (arat's own; never git/linear)
  2  usage error
  3  not found
  4  precondition failed (dirty / unpushed / non-empty scratch or project)
  5  conflict (already exists)
  6  external tool error (git, linear — nothing else maps here)
  7  config error
  130  interrupted (Ctrl-C / SIGTERM; cleanup has already run)
`,
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	state := &state{deps: d}
	root.PersistentFlags().StringVar(&state.configPath, "config", "", "path to config file (default: $ARAT_CONFIG, $XDG_CONFIG_HOME/arat/config.toml, $HOME/.config/arat/config.toml)")
	root.PersistentFlags().BoolVar(&state.jsonOut, "json", false, "emit JSON output where supported")
	// No backticks in the usage string: pflag renders backticked text as the
	// flag's value placeholder, which would make a boolean flag read as if
	// it took an argument.
	root.PersistentFlags().BoolVarP(&state.verbose, "verbose", "v", false, "emit per-step progress to stderr (one line per repo during new)")

	root.AddCommand(
		newLsCmd(state),
		newNewCmd(state),
		newRmCmd(state),
		newGoCmd(state),
		newAttachCmd(state),
		newDetachCmd(state),
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

// state carries the injected deps plus the persistent-flag values, which
// cobra writes straight into these fields — no pointer hop at read time.
type state struct {
	deps       Deps
	configPath string
	jsonOut    bool
	verbose    bool
}

// service builds the workspace service for cfg, mapping construction
// failures to the config exit code so they render and exit like every other
// classified error instead of short-circuiting the process inside a factory.
func (s *state) service(cfg *config.Config) (Service, error) {
	svc, err := s.deps.NewService(cfg)
	if err != nil {
		return nil, &exitErr{code: ExitConfig, err: err}
	}
	return svc, nil
}

func (s *state) writer() *output.Writer {
	w := &output.Writer{Out: s.deps.Stdout, Err: s.deps.Stderr, Format: output.Text}
	if s.jsonOut {
		w.Format = output.JSON
	}
	return w
}

// loadConfig resolves and loads the config, mapping ErrNotFound to a clear message.
func (s *state) loadConfig() (*config.Config, error) {
	path, err := config.ResolvePath(s.configPath)
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

// mapUnclassifiedError is the default branch of the per-command error
// mappers. Git and linear failures carry their packages' ErrCmd sentinel and
// keep the external-tool exit code; everything else (filesystem, wiring,
// arat's own bugs) exits generic. This is what keeps exit 6 meaning "git or
// linear failed" — the one code wrapper scripts may treat as retryable —
// instead of "anything arat didn't classify".
func mapUnclassifiedError(err error) *exitErr {
	if errors.Is(err, git.ErrCmd) || errors.Is(err, linear.ErrCmd) {
		return &exitErr{code: ExitExternal, err: err}
	}
	return &exitErr{code: ExitGeneric, err: err}
}

// exitErr wraps an error with the exit code arat should return.
type exitErr struct {
	code ExitCode
	err  error
}

func (e *exitErr) Error() string { return e.err.Error() }
func (e *exitErr) Unwrap() error { return e.err }

// Execute runs the root command under ctx and returns the desired process
// exit code. ctx is the cancellation root: main derives it from
// signal.NotifyContext so Ctrl-C reaches every subprocess and errgroup
// through cmd.Context().
func Execute(ctx context.Context, d Deps, args []string) ExitCode {
	if d.Stdout == nil {
		d.Stdout = os.Stdout
	}
	if d.Stderr == nil {
		d.Stderr = os.Stderr
	}
	// The two factories every command needs are wiring, not user input: a
	// nil one is a bug in the embedding binary, reported once here rather
	// than as a cryptic panic (or usage error) deep inside a handler. The
	// interactive deps stay optional — nil legitimately means "not
	// available", and isInteractive gates them.
	if d.NewConfig == nil || d.NewService == nil {
		fmt.Fprintln(d.Stderr, "error: arat wiring incomplete: NewConfig and NewService must be set")
		return ExitGeneric
	}
	cmd := Root(d)
	cmd.SetArgs(args)
	cmd.SetOut(d.Stdout)
	cmd.SetErr(d.Stderr)
	err := cmd.ExecuteContext(ctx)
	if err == nil {
		return ExitOK
	}
	fmt.Fprintf(d.Stderr, "error: %v\n", err)
	// A cancelled root context means the user interrupted the run; the error
	// above still prints (it may carry cleanup notes) but the exit code
	// reports the interruption, not whatever failure the cancellation caused.
	if ctx.Err() != nil {
		return ExitInterrupted
	}
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
