package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/data-pata/arat/internal/config"
	"github.com/data-pata/arat/internal/linear"
	"github.com/data-pata/arat/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeService implements cmd.Service for handler-level tests.
type fakeService struct {
	listResult       []workspace.Workspace
	listErr          error
	getResult        *workspace.Workspace
	getErr           error
	newResult        *workspace.Workspace
	newErr           error
	removeResult     *workspace.RemoveResult
	removeErr        error
	attachResult     *workspace.AttachResult
	attachErr        error
	addReposResult   *workspace.AddReposResult
	addReposErr      error
	candidatesResult []workspace.RepoCandidate
	candidatesErr    error

	getCalls         []string
	newCalls         []workspace.NewOptions
	removeCalls      []workspace.RemoveOptions
	attachCalls      []workspace.AttachOptions
	addReposCalls    []workspace.AddReposOptions
	candidatesCalled int
}

func (f *fakeService) List(_ context.Context) ([]workspace.Workspace, error) {
	return f.listResult, f.listErr
}
func (f *fakeService) Get(_ context.Context, name string) (*workspace.Workspace, error) {
	f.getCalls = append(f.getCalls, name)
	return f.getResult, f.getErr
}
func (f *fakeService) New(_ context.Context, opts workspace.NewOptions) (*workspace.Workspace, error) {
	f.newCalls = append(f.newCalls, opts)
	return f.newResult, f.newErr
}
func (f *fakeService) Remove(_ context.Context, opts workspace.RemoveOptions) (*workspace.RemoveResult, error) {
	f.removeCalls = append(f.removeCalls, opts)
	return f.removeResult, f.removeErr
}
func (f *fakeService) AttachTicket(_ context.Context, opts workspace.AttachOptions) (*workspace.AttachResult, error) {
	f.attachCalls = append(f.attachCalls, opts)
	return f.attachResult, f.attachErr
}
func (f *fakeService) AddRepos(_ context.Context, opts workspace.AddReposOptions) (*workspace.AddReposResult, error) {
	f.addReposCalls = append(f.addReposCalls, opts)
	return f.addReposResult, f.addReposErr
}
func (f *fakeService) ListRepoCandidates() ([]workspace.RepoCandidate, error) {
	f.candidatesCalled++
	return f.candidatesResult, f.candidatesErr
}

type runResult struct {
	stdout string
	stderr string
	exit   int
	svc    *fakeService
}

// run executes a cmd line with injected deps. cfg may be nil (a minimal one is
// supplied); svc may be nil (an empty fakeService is supplied).
func run(t *testing.T, args []string, cfg *config.Config, svc *fakeService) runResult {
	return runWithPicker(t, args, cfg, svc, nil)
}

func runWithPicker(t *testing.T, args []string, cfg *config.Config, svc *fakeService, picker func(context.Context, []workspace.Workspace, io.Writer) (*workspace.Workspace, error)) runResult {
	return runWithDeps(t, args, cfg, svc, depsOpts{picker: picker})
}

type depsOpts struct {
	picker   func(context.Context, []workspace.Workspace, io.Writer) (*workspace.Workspace, error)
	linear   *fakeLinear
	cwd      func() (string, error)
	tickFlow TicketFlow
	repoFlow RepoFlow
	isTTY    func() bool
}

func runWithDeps(t *testing.T, args []string, cfg *config.Config, svc *fakeService, opts depsOpts) runResult {
	t.Helper()
	if cfg == nil {
		cfg = &config.Config{Root: "/tmp", BranchPrefix: "ps", Linear: config.LinearConfig{Enabled: true, DefaultTeam: "ABC"}}
	}
	if svc == nil {
		svc = &fakeService{}
	}
	var stdout, stderr bytes.Buffer
	deps := Deps{
		Stdout: &stdout, Stderr: &stderr,
		NewConfig:     func(string) (*config.Config, error) { return cfg, nil },
		NewService:    func(*config.Config) Service { return svc },
		PickWorkspace: opts.picker,
		Cwd:           opts.cwd,
	}
	if opts.linear != nil {
		deps.NewLinear = func() LinearClient { return opts.linear }
	}
	deps.TicketFlow = opts.tickFlow
	deps.RepoFlow = opts.repoFlow
	deps.IsTTY = opts.isTTY
	exit := Execute(deps, args)
	return runResult{stdout.String(), stderr.String(), exit, svc}
}

// fakeLinear implements cmd.LinearClient for handler-level tests.
type fakeLinear struct {
	available    bool
	listResult   []linear.Issue
	listErr      error
	createResult linear.IssueResult
	createErr    error
	commentErr   error

	listCalls    []linear.IssueListOptions
	createCalls  []linear.IssueCreateOptions
	commentCalls []linear.CommentAddOptions
}

func (f *fakeLinear) Available(context.Context) error {
	if f.available {
		return nil
	}
	return errors.New("linear binary unavailable")
}
func (f *fakeLinear) IssueList(_ context.Context, opts linear.IssueListOptions) ([]linear.Issue, error) {
	f.listCalls = append(f.listCalls, opts)
	return f.listResult, f.listErr
}
func (f *fakeLinear) IssueCreate(_ context.Context, opts linear.IssueCreateOptions) (linear.IssueResult, error) {
	f.createCalls = append(f.createCalls, opts)
	return f.createResult, f.createErr
}
func (f *fakeLinear) CommentAdd(_ context.Context, opts linear.CommentAddOptions) error {
	f.commentCalls = append(f.commentCalls, opts)
	return f.commentErr
}

// --- ls --------------------------------------------------------------

func TestLs_text(t *testing.T) {
	svc := &fakeService{listResult: []workspace.Workspace{
		{Name: "x", Repos: []workspace.RepoStatus{{Name: "r", Branch: "b", Dirty: true, Unpushed: true, Stashes: 3}}},
	}}
	r := run(t, []string{"ls"}, nil, svc)
	assert.Equal(t, 0, r.exit, r.stderr)
	assert.Contains(t, r.stdout, "── x ──")
	assert.Contains(t, r.stdout, "*dirty*")
	assert.Contains(t, r.stdout, "*unpushed*")
	assert.Contains(t, r.stdout, "*stashes:3*")
}

func TestLs_json(t *testing.T) {
	svc := &fakeService{listResult: []workspace.Workspace{{Name: "x", Path: "/p"}}}
	r := run(t, []string{"ls", "--json"}, nil, svc)
	assert.Equal(t, 0, r.exit, r.stderr)
	assert.Contains(t, r.stdout, `"name": "x"`)
	assert.Contains(t, r.stdout, `"path": "/p"`)
}

func TestLs_empty(t *testing.T) {
	r := run(t, []string{"ls"}, nil, nil)
	assert.Equal(t, 0, r.exit)
	assert.Contains(t, r.stdout, "no workspaces")
}

func TestLs_noWorkspacesDir(t *testing.T) {
	svc := &fakeService{listErr: fmt.Errorf("%w: /tmp/x/feat", workspace.ErrNoWorkspacesDir)}
	r := run(t, []string{"ls"}, nil, svc)
	assert.Equal(t, 0, r.exit)
	assert.Contains(t, r.stderr, "no workspaces yet")
}

func TestLs_jsonNoWorkspacesDir(t *testing.T) {
	svc := &fakeService{listErr: fmt.Errorf("%w: /tmp/x/feat", workspace.ErrNoWorkspacesDir)}
	r := run(t, []string{"ls", "--json"}, nil, svc)
	assert.Equal(t, 0, r.exit)
	// JSON branch emits an empty array even when the dir is missing.
	assert.Equal(t, "[]\n", r.stdout)
}

func TestLs_serviceError(t *testing.T) {
	svc := &fakeService{listErr: fmt.Errorf("kaboom")}
	r := run(t, []string{"ls"}, nil, svc)
	assert.Equal(t, ExitExternal, r.exit)
	assert.Contains(t, r.stderr, "kaboom")
}

// --- new -------------------------------------------------------------

func TestNew_happy(t *testing.T) {
	svc := &fakeService{newResult: &workspace.Workspace{Name: "abc-1--x", Path: "/p", TicketURL: "https://t/ABC-1"}}
	r := run(t, []string{"new", "x", "--ticket", "abc-1"}, nil, svc)
	assert.Equal(t, 0, r.exit, r.stderr)
	assert.Equal(t, "/p\n", r.stdout)
	require.Len(t, svc.newCalls, 1)
	assert.Equal(t, "x", svc.newCalls[0].ShortName)
	assert.Equal(t, "abc-1", svc.newCalls[0].Ticket)
	assert.Contains(t, r.stderr, "ticket: https://t/ABC-1")
}

func TestNew_uppercaseTicketLowered(t *testing.T) {
	svc := &fakeService{newResult: &workspace.Workspace{Name: "ok", Path: "/p"}}
	r := run(t, []string{"new", "x", "--ticket", "ABC-1"}, nil, svc)
	assert.Equal(t, 0, r.exit, r.stderr)
	require.Len(t, svc.newCalls, 1)
	assert.Equal(t, "abc-1", svc.newCalls[0].Ticket, "ticket flag is lowercased before passing to service")
}

func TestNew_repos(t *testing.T) {
	svc := &fakeService{newResult: &workspace.Workspace{Name: "x", Path: "/p"}}
	r := run(t, []string{"new", "x", "--no-ticket", "--repos", "a,b,c"}, nil, svc)
	assert.Equal(t, 0, r.exit, r.stderr)
	require.Len(t, svc.newCalls, 1)
	assert.Equal(t, []string{"a", "b", "c"}, svc.newCalls[0].Repos)
}

func TestNew_mutuallyExclusiveTicketFlags(t *testing.T) {
	r := run(t, []string{"new", "x", "--ticket", "abc-1", "--no-ticket"}, nil, nil)
	assert.Equal(t, ExitUsage, r.exit)
	assert.Contains(t, r.stderr, "mutually exclusive")
}

func TestNew_conflict(t *testing.T) {
	svc := &fakeService{newErr: fmt.Errorf("%w: dup", workspace.ErrAlreadyExists)}
	r := run(t, []string{"new", "dup", "--no-ticket"}, nil, svc)
	assert.Equal(t, ExitConflict, r.exit)
}

func TestNew_unknownRepo(t *testing.T) {
	svc := &fakeService{newErr: fmt.Errorf("%w: missing not at root", workspace.ErrNotFound)}
	r := run(t, []string{"new", "x", "--no-ticket"}, nil, svc)
	assert.Equal(t, ExitNotFound, r.exit)
}

func TestNew_invalidShortNameFromService(t *testing.T) {
	svc := &fakeService{newErr: fmt.Errorf("%w: invalid short name %q: ...", workspace.ErrInvalidInput, "BAD")}
	r := run(t, []string{"new", "BAD", "--no-ticket"}, nil, svc)
	assert.Equal(t, ExitUsage, r.exit)
}

func TestNew_invalidTicketFromService(t *testing.T) {
	svc := &fakeService{newErr: fmt.Errorf("%w: ticket %q does not match pattern", workspace.ErrInvalidInput, "BAD")}
	r := run(t, []string{"new", "x", "--ticket", "BAD"}, nil, svc)
	assert.Equal(t, ExitUsage, r.exit)
}

func TestNew_argRequired(t *testing.T) {
	r := run(t, []string{"new"}, nil, nil)
	assert.NotEqual(t, 0, r.exit)
}

// --- new: phase 7 flags ----------------------------------------------

func TestNew_fromCurrent(t *testing.T) {
	dir := t.TempDir()
	wsDir := filepath.Join(dir, "feat")
	require.NoError(t, os.MkdirAll(filepath.Join(wsDir, "parent"), 0o755))
	cfg := &config.Config{Root: dir, WorkspacesDir: wsDir, BranchPrefix: "ps", Linear: config.LinearConfig{Enabled: true, DefaultTeam: "ABC"}}

	svc := &fakeService{
		newResult: &workspace.Workspace{Name: "child", Path: "/p"},
		getResult: &workspace.Workspace{Name: "parent", Repos: []workspace.RepoStatus{
			{Name: "repo-a", Branch: "ps--parent"},
			{Name: "repo-b", Branch: "ps--parent"},
		}},
	}
	cwdFn := func() (string, error) { return filepath.Join(wsDir, "parent", "repo-a"), nil }
	r := runWithDeps(t, []string{"new", "child", "--no-ticket", "--from-current"}, cfg, svc, depsOpts{cwd: cwdFn})
	assert.Equal(t, 0, r.exit, r.stderr)
	require.Len(t, svc.newCalls, 1)
	assert.Equal(t, map[string]string{"repo-a": "ps--parent", "repo-b": "ps--parent"}, svc.newCalls[0].BaseByRepo)
}

func TestNew_carryContext(t *testing.T) {
	dir := t.TempDir()
	wsDir := filepath.Join(dir, "feat")
	require.NoError(t, os.MkdirAll(filepath.Join(wsDir, "abc-1--parent"), 0o755))
	cfg := &config.Config{Root: dir, WorkspacesDir: wsDir, BranchPrefix: "ps", Linear: config.LinearConfig{Enabled: true, DefaultTeam: "ABC"}}

	svc := &fakeService{
		newResult: &workspace.Workspace{Name: "abc-2--child", Path: "/p"},
		getResult: &workspace.Workspace{Name: "abc-1--parent", ShortName: "parent", Ticket: "abc-1", TicketURL: "https://x/ABC-1"},
	}
	cwdFn := func() (string, error) { return filepath.Join(wsDir, "abc-1--parent"), nil }
	r := runWithDeps(t, []string{"new", "child", "--no-ticket", "--carry-context"}, cfg, svc, depsOpts{cwd: cwdFn})
	assert.Equal(t, 0, r.exit, r.stderr)
	require.Len(t, svc.newCalls, 1)
	require.NotNil(t, svc.newCalls[0].CarryFrom)
	assert.Equal(t, "abc-1--parent", svc.newCalls[0].CarryFrom.ParentName)
	assert.Equal(t, "abc-1", svc.newCalls[0].CarryFrom.ParentTicket)
	assert.Equal(t, "https://x/ABC-1", svc.newCalls[0].CarryFrom.ParentTicketURL)
}

func TestNew_fromCurrent_outsideWorkspace(t *testing.T) {
	dir := t.TempDir()
	wsDir := filepath.Join(dir, "feat")
	require.NoError(t, os.MkdirAll(wsDir, 0o755))
	cfg := &config.Config{Root: dir, WorkspacesDir: wsDir, BranchPrefix: "ps", Linear: config.LinearConfig{Enabled: true, DefaultTeam: "ABC"}}

	cwdFn := func() (string, error) { return "/tmp/elsewhere", nil }
	r := runWithDeps(t, []string{"new", "child", "--no-ticket", "--from-current"}, cfg, &fakeService{}, depsOpts{cwd: cwdFn})
	assert.Equal(t, ExitUsage, r.exit)
	assert.Contains(t, r.stderr, "not inside a workspace")
}

func TestNew_codeWorkspaceFlag(t *testing.T) {
	svc := &fakeService{newResult: &workspace.Workspace{Name: "x", Path: "/p"}}
	r := run(t, []string{"new", "x", "--no-ticket", "--code-workspace"}, nil, svc)
	assert.Equal(t, 0, r.exit, r.stderr)
	require.Len(t, svc.newCalls, 1)
	assert.True(t, svc.newCalls[0].GenerateCodeWorkspace)
}

// --- new: interactive ticket flow ------------------------------------

func TestNew_interactivePickAttachesTicket(t *testing.T) {
	svc := &fakeService{newResult: &workspace.Workspace{Name: "abc-9--x", Path: "/p"}}
	lc := &fakeLinear{available: true}
	flow := func(_ context.Context, _ linear.Reader, _ string, _ io.Writer) (TicketFlowResult, error) {
		return TicketFlowResult{Ticket: "ABC-9"}, nil
	}
	r := runWithDeps(t, []string{"new", "x"}, nil, svc, depsOpts{linear: lc, tickFlow: flow, isTTY: func() bool { return true }})
	assert.Equal(t, 0, r.exit, r.stderr)
	require.Len(t, svc.newCalls, 1)
	assert.Equal(t, "abc-9", svc.newCalls[0].Ticket, "ticket lowercased and passed through")
}

func TestNew_interactiveSkipped(t *testing.T) {
	svc := &fakeService{newResult: &workspace.Workspace{Name: "x", Path: "/p"}}
	lc := &fakeLinear{available: true}
	flow := func(_ context.Context, _ linear.Reader, _ string, _ io.Writer) (TicketFlowResult, error) {
		return TicketFlowResult{Skip: true}, nil
	}
	r := runWithDeps(t, []string{"new", "x"}, nil, svc, depsOpts{linear: lc, tickFlow: flow, isTTY: func() bool { return true }})
	assert.Equal(t, 0, r.exit, r.stderr)
	require.Len(t, svc.newCalls, 1)
	assert.Equal(t, "", svc.newCalls[0].Ticket)
}

func TestNew_interactiveCancelled(t *testing.T) {
	lc := &fakeLinear{available: true}
	flow := func(_ context.Context, _ linear.Reader, _ string, _ io.Writer) (TicketFlowResult, error) {
		return TicketFlowResult{Cancelled: true}, nil
	}
	r := runWithDeps(t, []string{"new", "x"}, nil, &fakeService{}, depsOpts{linear: lc, tickFlow: flow, isTTY: func() bool { return true }})
	assert.Equal(t, ExitUsage, r.exit)
}

func TestNew_interactiveHintPrinted(t *testing.T) {
	svc := &fakeService{newResult: &workspace.Workspace{Name: "x", Path: "/p"}}
	lc := &fakeLinear{available: true}
	flow := func(_ context.Context, _ linear.Reader, _ string, _ io.Writer) (TicketFlowResult, error) {
		return TicketFlowResult{Skip: true, Hint: "run `arat ticket create` first"}, nil
	}
	r := runWithDeps(t, []string{"new", "x"}, nil, svc, depsOpts{linear: lc, tickFlow: flow, isTTY: func() bool { return true }})
	assert.Equal(t, 0, r.exit, r.stderr)
	assert.Contains(t, r.stderr, "run `arat ticket create` first")
}

func TestNew_noFlowWhenNotTTY(t *testing.T) {
	svc := &fakeService{newResult: &workspace.Workspace{Name: "x", Path: "/p"}}
	lc := &fakeLinear{available: true}
	flowCalled := false
	flow := func(_ context.Context, _ linear.Reader, _ string, _ io.Writer) (TicketFlowResult, error) {
		flowCalled = true
		return TicketFlowResult{}, nil
	}
	r := runWithDeps(t, []string{"new", "x"}, nil, svc, depsOpts{linear: lc, tickFlow: flow, isTTY: func() bool { return false }})
	assert.Equal(t, 0, r.exit, r.stderr)
	assert.False(t, flowCalled, "flow must not run when stdin isn't a tty")
	assert.Empty(t, svc.newCalls[0].Ticket)
}

func TestNew_noFlowWhenLinearDisabled(t *testing.T) {
	cfg := &config.Config{Root: "/tmp", BranchPrefix: "ps", Linear: config.LinearConfig{Enabled: false}}
	svc := &fakeService{newResult: &workspace.Workspace{Name: "x", Path: "/p"}}
	flowCalled := false
	flow := func(_ context.Context, _ linear.Reader, _ string, _ io.Writer) (TicketFlowResult, error) {
		flowCalled = true
		return TicketFlowResult{}, nil
	}
	r := runWithDeps(t, []string{"new", "x"}, cfg, svc, depsOpts{linear: &fakeLinear{available: true}, tickFlow: flow, isTTY: func() bool { return true }})
	assert.Equal(t, 0, r.exit, r.stderr)
	assert.False(t, flowCalled, "flow must not run when linear is disabled in config")
}

func TestNew_explicitNoTicketSkipsFlow(t *testing.T) {
	svc := &fakeService{newResult: &workspace.Workspace{Name: "x", Path: "/p"}}
	flowCalled := false
	flow := func(_ context.Context, _ linear.Reader, _ string, _ io.Writer) (TicketFlowResult, error) {
		flowCalled = true
		return TicketFlowResult{}, nil
	}
	r := runWithDeps(t, []string{"new", "x", "--no-ticket"}, nil, svc, depsOpts{linear: &fakeLinear{available: true}, tickFlow: flow, isTTY: func() bool { return true }})
	assert.Equal(t, 0, r.exit, r.stderr)
	assert.False(t, flowCalled)
}

// --- new: interactive repo flow --------------------------------------

func TestNew_interactiveRepoPickerWiresSelection(t *testing.T) {
	svc := &fakeService{
		newResult: &workspace.Workspace{Name: "x", Path: "/p"},
		candidatesResult: []workspace.RepoCandidate{
			{Name: "core", Selected: true},
			{Name: "infra", Selected: false},
		},
	}
	flow := func(_ context.Context, cands []workspace.RepoCandidate, _ io.Writer) (RepoFlowResult, error) {
		// echo what the picker showed: confirm core+infra.
		require.Len(t, cands, 2)
		return RepoFlowResult{Repos: []string{"core", "infra"}}, nil
	}
	r := runWithDeps(t, []string{"new", "x", "--no-ticket"}, nil, svc, depsOpts{repoFlow: flow, isTTY: func() bool { return true }})
	assert.Equal(t, 0, r.exit, r.stderr)
	assert.Equal(t, 1, svc.candidatesCalled)
	require.Len(t, svc.newCalls, 1)
	assert.Equal(t, []string{"core", "infra"}, svc.newCalls[0].Repos)
}

func TestNew_interactiveRepoPickerCancelled(t *testing.T) {
	svc := &fakeService{candidatesResult: []workspace.RepoCandidate{{Name: "core", Selected: true}}}
	flow := func(_ context.Context, _ []workspace.RepoCandidate, _ io.Writer) (RepoFlowResult, error) {
		return RepoFlowResult{Cancelled: true}, nil
	}
	r := runWithDeps(t, []string{"new", "x", "--no-ticket"}, nil, svc, depsOpts{repoFlow: flow, isTTY: func() bool { return true }})
	assert.Equal(t, ExitUsage, r.exit)
	assert.Empty(t, svc.newCalls, "service.New must not run when the user cancelled the picker")
}

func TestNew_interactiveRepoPickerSkippedByExplicitRepos(t *testing.T) {
	svc := &fakeService{newResult: &workspace.Workspace{Name: "x", Path: "/p"}}
	flowCalled := false
	flow := func(_ context.Context, _ []workspace.RepoCandidate, _ io.Writer) (RepoFlowResult, error) {
		flowCalled = true
		return RepoFlowResult{}, nil
	}
	r := runWithDeps(t, []string{"new", "x", "--no-ticket", "--repos", "a,b"}, nil, svc, depsOpts{repoFlow: flow, isTTY: func() bool { return true }})
	assert.Equal(t, 0, r.exit, r.stderr)
	assert.False(t, flowCalled, "explicit --repos must short-circuit the picker")
	assert.Equal(t, 0, svc.candidatesCalled)
	require.Len(t, svc.newCalls, 1)
	assert.Equal(t, []string{"a", "b"}, svc.newCalls[0].Repos)
}

func TestNew_interactiveRepoPickerSkippedWhenNotTTY(t *testing.T) {
	svc := &fakeService{newResult: &workspace.Workspace{Name: "x", Path: "/p"}}
	flowCalled := false
	flow := func(_ context.Context, _ []workspace.RepoCandidate, _ io.Writer) (RepoFlowResult, error) {
		flowCalled = true
		return RepoFlowResult{}, nil
	}
	r := runWithDeps(t, []string{"new", "x", "--no-ticket"}, nil, svc, depsOpts{repoFlow: flow, isTTY: func() bool { return false }})
	assert.Equal(t, 0, r.exit, r.stderr)
	assert.False(t, flowCalled, "non-tty must keep the default+glob fallback")
	assert.Equal(t, 0, svc.candidatesCalled)
	require.Len(t, svc.newCalls, 1)
	assert.Empty(t, svc.newCalls[0].Repos, "empty repos -> service falls back to default+glob")
}

func TestNew_interactiveRepoPickerSkippedWhenNoCandidates(t *testing.T) {
	svc := &fakeService{
		newResult:        &workspace.Workspace{Name: "x", Path: "/p"},
		candidatesResult: nil, // empty root: no candidates to pick from
	}
	flowCalled := false
	flow := func(_ context.Context, _ []workspace.RepoCandidate, _ io.Writer) (RepoFlowResult, error) {
		flowCalled = true
		return RepoFlowResult{}, nil
	}
	r := runWithDeps(t, []string{"new", "x", "--no-ticket"}, nil, svc, depsOpts{repoFlow: flow, isTTY: func() bool { return true }})
	assert.Equal(t, 0, r.exit, r.stderr)
	assert.Equal(t, 1, svc.candidatesCalled)
	assert.False(t, flowCalled, "no candidates -> picker not invoked, service surfaces the empty-root error")
}

func TestNew_interactiveRepoPickerListErrorPropagates(t *testing.T) {
	svc := &fakeService{candidatesErr: errors.New("disk on fire")}
	flowCalled := false
	flow := func(_ context.Context, _ []workspace.RepoCandidate, _ io.Writer) (RepoFlowResult, error) {
		flowCalled = true
		return RepoFlowResult{}, nil
	}
	r := runWithDeps(t, []string{"new", "x", "--no-ticket"}, nil, svc, depsOpts{repoFlow: flow, isTTY: func() bool { return true }})
	assert.Equal(t, ExitExternal, r.exit)
	assert.Contains(t, r.stderr, "disk on fire")
	assert.False(t, flowCalled)
}

func TestNew_explicitTicketSkipsFlow(t *testing.T) {
	svc := &fakeService{newResult: &workspace.Workspace{Name: "x", Path: "/p"}}
	flowCalled := false
	flow := func(_ context.Context, _ linear.Reader, _ string, _ io.Writer) (TicketFlowResult, error) {
		flowCalled = true
		return TicketFlowResult{}, nil
	}
	r := runWithDeps(t, []string{"new", "x", "--ticket", "abc-1"}, nil, svc, depsOpts{linear: &fakeLinear{available: true}, tickFlow: flow, isTTY: func() bool { return true }})
	assert.Equal(t, 0, r.exit, r.stderr)
	assert.False(t, flowCalled)
}

func TestNew_json(t *testing.T) {
	svc := &fakeService{newResult: &workspace.Workspace{Name: "x", Path: "/p"}}
	r := run(t, []string{"new", "x", "--no-ticket", "--json"}, nil, svc)
	assert.Equal(t, 0, r.exit, r.stderr)
	assert.Contains(t, r.stdout, `"name": "x"`)
	assert.Contains(t, r.stdout, `"path": "/p"`)
}

// --- rm --------------------------------------------------------------

func TestRm_happy(t *testing.T) {
	svc := &fakeService{}
	r := run(t, []string{"rm", "x"}, nil, svc)
	assert.Equal(t, 0, r.exit, r.stderr)
	assert.Contains(t, r.stderr, "removed workspace x")
	require.Len(t, svc.removeCalls, 1)
	assert.False(t, svc.removeCalls[0].Force)
	assert.False(t, svc.removeCalls[0].KeepBranches)
}

func TestRm_force(t *testing.T) {
	svc := &fakeService{}
	r := run(t, []string{"rm", "x", "--force"}, nil, svc)
	assert.Equal(t, 0, r.exit, r.stderr)
	require.Len(t, svc.removeCalls, 1)
	assert.True(t, svc.removeCalls[0].Force)
}

func TestRm_keepBranches(t *testing.T) {
	svc := &fakeService{}
	r := run(t, []string{"rm", "x", "--keep-branches"}, nil, svc)
	assert.Equal(t, 0, r.exit, r.stderr)
	require.Len(t, svc.removeCalls, 1)
	assert.True(t, svc.removeCalls[0].KeepBranches)
}

func TestRm_alias_kill(t *testing.T) {
	svc := &fakeService{}
	r := run(t, []string{"kill", "x"}, nil, svc)
	assert.Equal(t, 0, r.exit, r.stderr)
	require.Len(t, svc.removeCalls, 1)
}

func TestRm_notFound(t *testing.T) {
	svc := &fakeService{removeErr: fmt.Errorf("%w: gone", workspace.ErrNotFound)}
	r := run(t, []string{"rm", "gone"}, nil, svc)
	assert.Equal(t, ExitNotFound, r.exit)
}

func TestRm_precondition(t *testing.T) {
	svc := &fakeService{removeErr: &workspace.ErrPrecondition{Reasons: []string{"/p: uncommitted changes", "/p: unpushed commits on ps--x"}}}
	r := run(t, []string{"rm", "x"}, nil, svc)
	assert.Equal(t, ExitPrecondition, r.exit)
	assert.Contains(t, r.stderr, "uncommitted changes")
	assert.Contains(t, r.stderr, "--force to override")
}

func TestRm_stashedReposSurfaceAsNote(t *testing.T) {
	svc := &fakeService{removeResult: &workspace.RemoveResult{
		StashedRepos: []workspace.StashedRepo{
			{Path: "/ws/x/repo-a", CanonicalRepo: "/root/repo-a", Stashes: 1},
			{Path: "/ws/x/repo-b", CanonicalRepo: "/root/repo-b", Stashes: 3},
		},
	}}
	r := run(t, []string{"rm", "x"}, nil, svc)
	assert.Equal(t, 0, r.exit, r.stderr)
	assert.Contains(t, r.stderr, "removed workspace x")
	assert.Contains(t, r.stderr, "1 stash entry preserved in /root/repo-a")
	assert.Contains(t, r.stderr, "3 stash entries preserved in /root/repo-b")
	assert.Contains(t, r.stderr, "git -C /root/repo-a stash list")
}

func TestRm_noStashedReposNoNote(t *testing.T) {
	svc := &fakeService{removeResult: &workspace.RemoveResult{}}
	r := run(t, []string{"rm", "x"}, nil, svc)
	assert.Equal(t, 0, r.exit, r.stderr)
	assert.NotContains(t, r.stderr, "preserved", "no stashed repos -> no note line")
}

// --- go --------------------------------------------------------------

func TestGo_byName(t *testing.T) {
	svc := &fakeService{getResult: &workspace.Workspace{Name: "x", Path: "/wsx/x"}}
	r := run(t, []string{"go", "x"}, nil, svc)
	assert.Equal(t, 0, r.exit, r.stderr)
	assert.Equal(t, "/wsx/x\n", r.stdout)
	assert.Equal(t, []string{"x"}, svc.getCalls)
}

func TestGo_notFound(t *testing.T) {
	svc := &fakeService{getErr: fmt.Errorf("%w: gone", workspace.ErrNotFound)}
	r := run(t, []string{"go", "gone"}, nil, svc)
	assert.Equal(t, ExitNotFound, r.exit)
}

func TestGo_noNameNoPickerWired(t *testing.T) {
	// run() doesn't wire PickWorkspace by default, so the picker should
	// refuse cleanly rather than panic.
	r := run(t, []string{"go"}, nil, nil)
	assert.Equal(t, ExitUsage, r.exit)
	assert.Contains(t, r.stderr, "no PickWorkspace impl wired")
}

func TestGo_pickerHappy(t *testing.T) {
	svc := &fakeService{listResult: []workspace.Workspace{{Name: "a", Path: "/a"}, {Name: "b", Path: "/b"}}}
	chosen := &workspace.Workspace{Name: "b", Path: "/b"}
	r := runWithPicker(t, []string{"go"}, nil, svc, func(_ context.Context, items []workspace.Workspace, _ io.Writer) (*workspace.Workspace, error) {
		require.Len(t, items, 2)
		return chosen, nil
	})
	assert.Equal(t, 0, r.exit, r.stderr)
	assert.Equal(t, "/b\n", r.stdout)
}

func TestGo_pickerCancelled(t *testing.T) {
	svc := &fakeService{listResult: []workspace.Workspace{{Name: "a", Path: "/a"}}}
	r := runWithPicker(t, []string{"go"}, nil, svc, func(context.Context, []workspace.Workspace, io.Writer) (*workspace.Workspace, error) {
		return nil, nil // user cancelled
	})
	assert.Equal(t, 0, r.exit, "cancelling the picker is not an error")
	assert.Empty(t, r.stdout, "no path is printed on cancel")
}

func TestGo_pickerNoWorkspaces(t *testing.T) {
	svc := &fakeService{listErr: fmt.Errorf("%w: x", workspace.ErrNoWorkspacesDir)}
	r := runWithPicker(t, []string{"go"}, nil, svc, func(context.Context, []workspace.Workspace, io.Writer) (*workspace.Workspace, error) {
		t.Fatal("picker must not be called when there are no workspaces")
		return nil, nil
	})
	assert.Equal(t, ExitNotFound, r.exit)
}

func TestGo_pickerError(t *testing.T) {
	svc := &fakeService{listResult: []workspace.Workspace{{Name: "a", Path: "/a"}}}
	r := runWithPicker(t, []string{"go"}, nil, svc, func(context.Context, []workspace.Workspace, io.Writer) (*workspace.Workspace, error) {
		return nil, fmt.Errorf("tui blew up")
	})
	assert.Equal(t, ExitExternal, r.exit)
	assert.Contains(t, r.stderr, "tui blew up")
}

func TestGo_printFlagAccepted(t *testing.T) {
	svc := &fakeService{getResult: &workspace.Workspace{Name: "x", Path: "/p"}}
	r := run(t, []string{"go", "--print", "x"}, nil, svc)
	assert.Equal(t, 0, r.exit, r.stderr)
	assert.Equal(t, "/p\n", r.stdout)
}

// --- init ------------------------------------------------------------

func TestInit_zsh(t *testing.T) {
	r := run(t, []string{"init", "zsh"}, nil, nil)
	assert.Equal(t, 0, r.exit, r.stderr)
	assert.Contains(t, r.stdout, "command arat")
	assert.Contains(t, r.stdout, "zsh")
}

func TestInit_bash(t *testing.T) {
	r := run(t, []string{"init", "bash"}, nil, nil)
	assert.Equal(t, 0, r.exit, r.stderr)
	assert.Contains(t, r.stdout, "command arat")
	assert.Contains(t, r.stdout, "bash")
}

func TestInit_fish(t *testing.T) {
	r := run(t, []string{"init", "fish"}, nil, nil)
	assert.Equal(t, 0, r.exit, r.stderr)
	assert.Contains(t, r.stdout, "function arat")
}

func TestInit_unknownShell(t *testing.T) {
	r := run(t, []string{"init", "nu"}, nil, nil)
	assert.Equal(t, ExitUsage, r.exit)
}

// --- ticket attach ---------------------------------------------------

func TestTicketAttach_happy(t *testing.T) {
	svc := &fakeService{attachResult: &workspace.AttachResult{
		Workspace: &workspace.Workspace{Name: "abc-1--x", Path: "/p", Ticket: "abc-1"},
	}}
	r := runWithDeps(t, []string{"ticket", "attach", "x", "abc-1"}, nil, svc, depsOpts{})
	assert.Equal(t, 0, r.exit, r.stderr)
	assert.Equal(t, "/p\n", r.stdout)
	assert.Contains(t, r.stderr, "attached ABC-1 → abc-1--x")
	require.Len(t, svc.attachCalls, 1)
	assert.Equal(t, "x", svc.attachCalls[0].Name)
	assert.Equal(t, "abc-1", svc.attachCalls[0].Ticket, "ticket lowercased")
}

func TestTicketAttach_uppercaseTicketLowered(t *testing.T) {
	svc := &fakeService{attachResult: &workspace.AttachResult{Workspace: &workspace.Workspace{Name: "abc-1--x", Path: "/p"}}}
	r := runWithDeps(t, []string{"ticket", "attach", "x", "ABC-1"}, nil, svc, depsOpts{})
	assert.Equal(t, 0, r.exit, r.stderr)
	require.Len(t, svc.attachCalls, 1)
	assert.Equal(t, "abc-1", svc.attachCalls[0].Ticket)
}

func TestTicketAttach_warningsPrinted(t *testing.T) {
	svc := &fakeService{attachResult: &workspace.AttachResult{
		Workspace: &workspace.Workspace{Name: "abc-1--x", Path: "/p", Ticket: "abc-1"},
		Warnings:  []workspace.AttachWarning{{Repo: "repo-b", Reason: "off branch"}},
	}}
	r := runWithDeps(t, []string{"ticket", "attach", "x", "abc-1"}, nil, svc, depsOpts{})
	assert.Equal(t, 0, r.exit, r.stderr)
	assert.Contains(t, r.stderr, "repo-b: off branch")
}

func TestTicketAttach_notFound(t *testing.T) {
	svc := &fakeService{attachErr: fmt.Errorf("%w: gone", workspace.ErrNotFound)}
	r := runWithDeps(t, []string{"ticket", "attach", "gone", "abc-1"}, nil, svc, depsOpts{})
	assert.Equal(t, ExitNotFound, r.exit)
}

func TestTicketAttach_alreadyExists(t *testing.T) {
	svc := &fakeService{attachErr: fmt.Errorf("%w: dup", workspace.ErrAlreadyExists)}
	r := runWithDeps(t, []string{"ticket", "attach", "x", "abc-1"}, nil, svc, depsOpts{})
	assert.Equal(t, ExitConflict, r.exit)
}

func TestTicketAttach_alreadyTicketed(t *testing.T) {
	svc := &fakeService{attachErr: &workspace.ErrPrecondition{Reasons: []string{"x has abc-2"}}}
	r := runWithDeps(t, []string{"ticket", "attach", "x", "abc-1"}, nil, svc, depsOpts{})
	assert.Equal(t, ExitPrecondition, r.exit)
}

func TestTicketAttach_badTicket(t *testing.T) {
	svc := &fakeService{attachErr: fmt.Errorf("%w: ticket %q does not match pattern", workspace.ErrInvalidInput, "bad")}
	r := runWithDeps(t, []string{"ticket", "attach", "x", "bad"}, nil, svc, depsOpts{})
	assert.Equal(t, ExitUsage, r.exit)
}

// --- repo add --------------------------------------------------------

func TestRepoAdd_explicitWorkspace(t *testing.T) {
	dir := t.TempDir()
	wsDir := filepath.Join(dir, "feat")
	require.NoError(t, os.MkdirAll(filepath.Join(wsDir, "abc-1--x"), 0o755))
	cfg := &config.Config{Root: dir, WorkspacesDir: wsDir, BranchPrefix: "ps", Linear: config.LinearConfig{Enabled: true, DefaultTeam: "ABC"}}

	svc := &fakeService{addReposResult: &workspace.AddReposResult{
		Workspace: &workspace.Workspace{Name: "abc-1--x", Path: filepath.Join(wsDir, "abc-1--x")},
		Added: []workspace.RepoStatus{
			{Name: "repo-b", Path: filepath.Join(wsDir, "abc-1--x", "repo-b"), Branch: "ps--x--abc-1"},
		},
	}}
	r := runWithDeps(t, []string{"repo", "add", "--workspace", "abc-1--x", "repo-b"}, cfg, svc, depsOpts{cwd: failingCwd(t)})
	assert.Equal(t, 0, r.exit, r.stderr)
	require.Len(t, svc.addReposCalls, 1)
	assert.Equal(t, "abc-1--x", svc.addReposCalls[0].Workspace)
	assert.Equal(t, []string{"repo-b"}, svc.addReposCalls[0].Repos)
	assert.Contains(t, r.stdout, filepath.Join(wsDir, "abc-1--x", "repo-b"))
	assert.Contains(t, r.stderr, "added 1 repo(s) to abc-1--x")
}

func TestRepoAdd_inferFromCwd(t *testing.T) {
	dir := t.TempDir()
	wsDir := filepath.Join(dir, "feat")
	require.NoError(t, os.MkdirAll(filepath.Join(wsDir, "myws", "repo-a"), 0o755))
	cfg := &config.Config{Root: dir, WorkspacesDir: wsDir, BranchPrefix: "ps", Linear: config.LinearConfig{Enabled: true, DefaultTeam: "ABC"}}

	svc := &fakeService{addReposResult: &workspace.AddReposResult{
		Workspace: &workspace.Workspace{Name: "myws"},
		Added:     []workspace.RepoStatus{{Name: "repo-b", Path: "/p", Branch: "ps--myws"}},
	}}
	cwdFn := func() (string, error) { return filepath.Join(wsDir, "myws", "repo-a"), nil }
	r := runWithDeps(t, []string{"repo", "add", "repo-b"}, cfg, svc, depsOpts{cwd: cwdFn})
	assert.Equal(t, 0, r.exit, r.stderr)
	require.Len(t, svc.addReposCalls, 1)
	assert.Equal(t, "myws", svc.addReposCalls[0].Workspace)
}

func TestRepoAdd_multipleRepos(t *testing.T) {
	svc := &fakeService{addReposResult: &workspace.AddReposResult{
		Workspace: &workspace.Workspace{Name: "x"},
		Added: []workspace.RepoStatus{
			{Name: "repo-b", Path: "/x/repo-b", Branch: "ps--x"},
			{Name: "repo-c", Path: "/x/repo-c", Branch: "ps--x"},
		},
	}}
	r := runWithDeps(t, []string{"repo", "add", "--workspace", "x", "repo-b", "repo-c"}, nil, svc, depsOpts{cwd: failingCwd(t)})
	assert.Equal(t, 0, r.exit, r.stderr)
	require.Len(t, svc.addReposCalls, 1)
	assert.Equal(t, []string{"repo-b", "repo-c"}, svc.addReposCalls[0].Repos)
}

func TestRepoAdd_baseFlag(t *testing.T) {
	svc := &fakeService{addReposResult: &workspace.AddReposResult{
		Workspace: &workspace.Workspace{Name: "x"},
		Added:     []workspace.RepoStatus{{Name: "repo-b", Path: "/p", Branch: "ps--x"}},
	}}
	r := runWithDeps(t, []string{"repo", "add", "--workspace", "x", "--base", "origin/main", "repo-b"}, nil, svc, depsOpts{cwd: failingCwd(t)})
	assert.Equal(t, 0, r.exit, r.stderr)
	require.Len(t, svc.addReposCalls, 1)
	assert.Equal(t, "origin/main", svc.addReposCalls[0].Base)
}

func TestRepoAdd_workspaceNotFound(t *testing.T) {
	svc := &fakeService{addReposErr: fmt.Errorf("%w: gone", workspace.ErrNotFound)}
	r := runWithDeps(t, []string{"repo", "add", "--workspace", "gone", "repo-b"}, nil, svc, depsOpts{cwd: failingCwd(t)})
	assert.Equal(t, ExitNotFound, r.exit)
}

func TestRepoAdd_alreadyExists(t *testing.T) {
	svc := &fakeService{addReposErr: fmt.Errorf("%w: dup", workspace.ErrAlreadyExists)}
	r := runWithDeps(t, []string{"repo", "add", "--workspace", "x", "repo-b"}, nil, svc, depsOpts{cwd: failingCwd(t)})
	assert.Equal(t, ExitConflict, r.exit)
}

func TestRepoAdd_singleRepoLayoutRejected(t *testing.T) {
	svc := &fakeService{addReposErr: &workspace.ErrPrecondition{Reasons: []string{"workspace x is a single-repo layout"}}}
	r := runWithDeps(t, []string{"repo", "add", "--workspace", "x", "repo-b"}, nil, svc, depsOpts{cwd: failingCwd(t)})
	assert.Equal(t, ExitPrecondition, r.exit)
	assert.Contains(t, r.stderr, "single-repo layout")
}

func TestRepoAdd_outsideWorkspace(t *testing.T) {
	dir := t.TempDir()
	wsDir := filepath.Join(dir, "feat")
	require.NoError(t, os.MkdirAll(wsDir, 0o755))
	cfg := &config.Config{Root: dir, WorkspacesDir: wsDir, BranchPrefix: "ps", Linear: config.LinearConfig{Enabled: true, DefaultTeam: "ABC"}}

	cwdFn := func() (string, error) { return "/tmp/elsewhere", nil }
	r := runWithDeps(t, []string{"repo", "add", "repo-b"}, cfg, &fakeService{}, depsOpts{cwd: cwdFn})
	assert.Equal(t, ExitUsage, r.exit)
	assert.Contains(t, r.stderr, "not inside a workspace")
}

func TestRepoAdd_argRequired(t *testing.T) {
	r := runWithDeps(t, []string{"repo", "add", "--workspace", "x"}, nil, nil, depsOpts{cwd: failingCwd(t)})
	assert.NotEqual(t, 0, r.exit)
}

// --- ticket create ---------------------------------------------------

func TestTicketCreate_happy(t *testing.T) {
	lc := &fakeLinear{available: true, createResult: linear.IssueResult{ID: "ABC-9999", Raw: "Created ABC-9999"}}
	r := runWithDeps(t, []string{"ticket", "create", "--title", "Fix it", "--project", "Side"}, nil, nil, depsOpts{linear: lc})
	assert.Equal(t, 0, r.exit, r.stderr)
	assert.Equal(t, "ABC-9999\n", r.stdout)
	require.Len(t, lc.createCalls, 1)
	assert.Equal(t, "Fix it", lc.createCalls[0].Title)
	assert.Equal(t, "ABC", lc.createCalls[0].Team, "team falls back to config default")
	assert.Equal(t, "Backlog", lc.createCalls[0].State, "state defaults to Backlog")
	assert.Equal(t, "Side", lc.createCalls[0].Project)
}

func TestTicketCreate_explicitTeamAndState(t *testing.T) {
	lc := &fakeLinear{available: true, createResult: linear.IssueResult{ID: "ENG-1"}}
	r := runWithDeps(t, []string{"ticket", "create", "--title", "x", "--team", "ENG", "--state", "In Progress"}, nil, nil, depsOpts{linear: lc})
	assert.Equal(t, 0, r.exit, r.stderr)
	require.Len(t, lc.createCalls, 1)
	assert.Equal(t, "ENG", lc.createCalls[0].Team)
	assert.Equal(t, "In Progress", lc.createCalls[0].State)
}

func TestTicketCreate_titleRequired(t *testing.T) {
	lc := &fakeLinear{available: true}
	r := runWithDeps(t, []string{"ticket", "create"}, nil, nil, depsOpts{linear: lc})
	assert.NotEqual(t, 0, r.exit)
	assert.Empty(t, lc.createCalls, "linear must not be invoked without title")
}

func TestTicketCreate_linearMissing(t *testing.T) {
	lc := &fakeLinear{available: false}
	r := runWithDeps(t, []string{"ticket", "create", "--title", "x"}, nil, nil, depsOpts{linear: lc})
	assert.Equal(t, ExitExternal, r.exit)
	assert.Contains(t, r.stderr, "linear")
}

func TestTicketCreate_noIDParsed(t *testing.T) {
	lc := &fakeLinear{available: true, createResult: linear.IssueResult{Raw: "weird output without an id"}}
	r := runWithDeps(t, []string{"ticket", "create", "--title", "x"}, nil, nil, depsOpts{linear: lc})
	assert.Equal(t, ExitExternal, r.exit)
	assert.Contains(t, r.stderr, "could not be parsed")
}

func TestTicketCreate_disabledByConfig(t *testing.T) {
	cfg := &config.Config{Root: "/tmp", BranchPrefix: "ps", Linear: config.LinearConfig{Enabled: false}}
	r := runWithDeps(t, []string{"ticket", "create", "--title", "x"}, cfg, nil, depsOpts{linear: &fakeLinear{available: true}})
	assert.Equal(t, ExitUsage, r.exit)
	assert.Contains(t, r.stderr, "linear is disabled")
}

// --- note ------------------------------------------------------------

func TestNote_explicitName(t *testing.T) {
	dir := t.TempDir()
	wsDir := filepath.Join(dir, "feat")
	require.NoError(t, os.MkdirAll(filepath.Join(wsDir, "abc-1--x"), 0o755))
	cfg := &config.Config{Root: dir, WorkspacesDir: wsDir, BranchPrefix: "ps", Linear: config.LinearConfig{Enabled: true, DefaultTeam: "ABC"}}

	svc := &fakeService{getResult: &workspace.Workspace{Name: "abc-1--x", Ticket: "abc-1"}}
	lc := &fakeLinear{available: true}
	r := runWithDeps(t, []string{"note", "abc-1--x", "wip"}, cfg, svc, depsOpts{linear: lc, cwd: failingCwd(t)})
	assert.Equal(t, 0, r.exit, r.stderr)
	require.Len(t, lc.commentCalls, 1)
	assert.Equal(t, "abc-1", lc.commentCalls[0].IssueID)
	assert.Equal(t, "wip", lc.commentCalls[0].Body)
	assert.Contains(t, r.stderr, "commented on ABC-1")
}

func TestNote_explicitName_notADir_treatedAsBody(t *testing.T) {
	// The first arg "abc-1--x" doesn't exist as a directory, so we expect cwd
	// inference. With cwd inside <ws>/sub, name should resolve to "wsX".
	dir := t.TempDir()
	wsDir := filepath.Join(dir, "feat")
	require.NoError(t, os.MkdirAll(filepath.Join(wsDir, "wsX", "sub"), 0o755))
	cfg := &config.Config{Root: dir, WorkspacesDir: wsDir, BranchPrefix: "ps", Linear: config.LinearConfig{Enabled: true, DefaultTeam: "ABC"}}

	svc := &fakeService{getResult: &workspace.Workspace{Name: "wsX", Ticket: "abc-1"}}
	lc := &fakeLinear{available: true}
	cwdFn := func() (string, error) { return filepath.Join(wsDir, "wsX", "sub"), nil }
	r := runWithDeps(t, []string{"note", "definitely", "not", "a", "ws"}, cfg, svc, depsOpts{linear: lc, cwd: cwdFn})
	assert.Equal(t, 0, r.exit, r.stderr)
	require.Len(t, lc.commentCalls, 1)
	assert.Equal(t, "definitely not a ws", lc.commentCalls[0].Body)
}

func TestNote_inferFromCwd(t *testing.T) {
	dir := t.TempDir()
	wsDir := filepath.Join(dir, "feat")
	require.NoError(t, os.MkdirAll(filepath.Join(wsDir, "myws"), 0o755))
	cfg := &config.Config{Root: dir, WorkspacesDir: wsDir, BranchPrefix: "ps", Linear: config.LinearConfig{Enabled: true, DefaultTeam: "ABC"}}

	svc := &fakeService{getResult: &workspace.Workspace{Name: "myws", Ticket: "abc-9"}}
	lc := &fakeLinear{available: true}
	cwdFn := func() (string, error) { return filepath.Join(wsDir, "myws"), nil }
	r := runWithDeps(t, []string{"note", "looks", "good"}, cfg, svc, depsOpts{linear: lc, cwd: cwdFn})
	assert.Equal(t, 0, r.exit, r.stderr)
	require.Len(t, lc.commentCalls, 1)
	assert.Equal(t, "abc-9", lc.commentCalls[0].IssueID)
	assert.Equal(t, "looks good", lc.commentCalls[0].Body)
}

func TestNote_inferFromCwd_outside(t *testing.T) {
	dir := t.TempDir()
	wsDir := filepath.Join(dir, "feat")
	require.NoError(t, os.MkdirAll(wsDir, 0o755))
	cfg := &config.Config{Root: dir, WorkspacesDir: wsDir, BranchPrefix: "ps", Linear: config.LinearConfig{Enabled: true, DefaultTeam: "ABC"}}

	cwdFn := func() (string, error) { return "/tmp/elsewhere", nil }
	r := runWithDeps(t, []string{"note", "x"}, cfg, &fakeService{}, depsOpts{linear: &fakeLinear{available: true}, cwd: cwdFn})
	assert.Equal(t, ExitUsage, r.exit)
	assert.Contains(t, r.stderr, "not inside a workspace")
}

func TestNote_workspaceWithoutTicket(t *testing.T) {
	dir := t.TempDir()
	wsDir := filepath.Join(dir, "feat")
	require.NoError(t, os.MkdirAll(filepath.Join(wsDir, "noticket"), 0o755))
	cfg := &config.Config{Root: dir, WorkspacesDir: wsDir, BranchPrefix: "ps", Linear: config.LinearConfig{Enabled: true, DefaultTeam: "ABC"}}

	svc := &fakeService{getResult: &workspace.Workspace{Name: "noticket"}} // Ticket=""
	lc := &fakeLinear{available: true}
	r := runWithDeps(t, []string{"note", "noticket", "hi"}, cfg, svc, depsOpts{linear: lc, cwd: failingCwd(t)})
	assert.Equal(t, ExitPrecondition, r.exit)
	assert.Contains(t, r.stderr, "no ticket attached")
}

func TestNote_linearDisabled(t *testing.T) {
	cfg := &config.Config{Root: "/tmp", BranchPrefix: "ps", Linear: config.LinearConfig{Enabled: false}}
	r := runWithDeps(t, []string{"note", "x"}, cfg, nil, depsOpts{linear: &fakeLinear{available: true}, cwd: failingCwd(t)})
	assert.Equal(t, ExitUsage, r.exit)
	assert.Contains(t, r.stderr, "linear is disabled")
}

func TestNote_explicitName_butNoBody(t *testing.T) {
	dir := t.TempDir()
	wsDir := filepath.Join(dir, "feat")
	require.NoError(t, os.MkdirAll(filepath.Join(wsDir, "x"), 0o755))
	cfg := &config.Config{Root: dir, WorkspacesDir: wsDir, BranchPrefix: "ps", Linear: config.LinearConfig{Enabled: true, DefaultTeam: "ABC"}}
	r := runWithDeps(t, []string{"note", "x"}, cfg, &fakeService{}, depsOpts{linear: &fakeLinear{available: true}, cwd: failingCwd(t)})
	assert.Equal(t, ExitUsage, r.exit)
	assert.Contains(t, r.stderr, "note text is required")
}

func TestNote_cwdResolverFails(t *testing.T) {
	dir := t.TempDir()
	wsDir := filepath.Join(dir, "feat")
	require.NoError(t, os.MkdirAll(wsDir, 0o755))
	cfg := &config.Config{Root: dir, WorkspacesDir: wsDir, BranchPrefix: "ps", Linear: config.LinearConfig{Enabled: true, DefaultTeam: "ABC"}}
	cwdFn := func() (string, error) { return "", errors.New("cwd boom") }
	r := runWithDeps(t, []string{"note", "x"}, cfg, &fakeService{}, depsOpts{linear: &fakeLinear{available: true}, cwd: cwdFn})
	assert.Equal(t, ExitUsage, r.exit)
	assert.Contains(t, r.stderr, "cwd boom")
}

func TestNote_cwdResolverMissing(t *testing.T) {
	dir := t.TempDir()
	wsDir := filepath.Join(dir, "feat")
	require.NoError(t, os.MkdirAll(wsDir, 0o755))
	cfg := &config.Config{Root: dir, WorkspacesDir: wsDir, BranchPrefix: "ps", Linear: config.LinearConfig{Enabled: true, DefaultTeam: "ABC"}}
	r := runWithDeps(t, []string{"note", "x"}, cfg, &fakeService{}, depsOpts{linear: &fakeLinear{available: true}})
	assert.Equal(t, ExitUsage, r.exit)
	assert.Contains(t, r.stderr, "cwd resolver not configured")
}

func TestNote_linearMissing(t *testing.T) {
	dir := t.TempDir()
	wsDir := filepath.Join(dir, "feat")
	require.NoError(t, os.MkdirAll(filepath.Join(wsDir, "x"), 0o755))
	cfg := &config.Config{Root: dir, WorkspacesDir: wsDir, BranchPrefix: "ps", Linear: config.LinearConfig{Enabled: true, DefaultTeam: "ABC"}}

	svc := &fakeService{getResult: &workspace.Workspace{Name: "x", Ticket: "abc-1"}}
	lc := &fakeLinear{available: false}
	r := runWithDeps(t, []string{"note", "x", "hi"}, cfg, svc, depsOpts{linear: lc, cwd: failingCwd(t)})
	assert.Equal(t, ExitExternal, r.exit)
	assert.Contains(t, r.stderr, "linear")
}

// failingCwd returns a Cwd resolver that fails the test if invoked. Use when
// the test scenario should never need to consult cwd.
func failingCwd(t *testing.T) func() (string, error) {
	return func() (string, error) {
		t.Helper()
		t.Fatal("cwd resolver must not be called in this scenario")
		return "", nil
	}
}

// --- version ---------------------------------------------------------

func TestVersion(t *testing.T) {
	r := run(t, []string{"version"}, nil, nil)
	assert.Equal(t, 0, r.exit)
	assert.Regexp(t, `^arat .+\n$`, r.stdout)
}

// --- config ----------------------------------------------------------

func TestConfigPath(t *testing.T) {
	t.Setenv("ARAT_CONFIG", "/from/env.toml")
	r := run(t, []string{"config", "path"}, nil, nil)
	assert.Equal(t, 0, r.exit)
	assert.Equal(t, "/from/env.toml\n", r.stdout)
}

func TestConfigInit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.toml")
	r := run(t, []string{"--config", path, "config", "init"}, nil, nil)
	assert.Equal(t, 0, r.exit, r.stderr)
	assert.Equal(t, path+"\n", r.stdout)
	_, err := os.Stat(path)
	require.NoError(t, err)
}

func TestConfigInit_existsConflict(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.toml")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
	r := run(t, []string{"--config", path, "config", "init"}, nil, nil)
	assert.Equal(t, ExitConflict, r.exit)

	r = run(t, []string{"--config", path, "config", "init", "--force"}, nil, nil)
	assert.Equal(t, 0, r.exit, r.stderr)
}

// --- global flags / wiring ------------------------------------------

func TestMissingConfig(t *testing.T) {
	deps := Deps{
		Stdout: io.Discard, Stderr: io.Discard,
		NewConfig:  func(string) (*config.Config, error) { return nil, fmt.Errorf("%w: x", config.ErrNotFound) },
		NewService: func(*config.Config) Service { return &fakeService{} },
	}
	assert.Equal(t, ExitConfig, Execute(deps, []string{"ls"}))
}

func TestUnknownCommand(t *testing.T) {
	r := run(t, []string{"nope"}, nil, nil)
	assert.Equal(t, ExitGeneric, r.exit, "uncategorized cobra errors fall through to ExitGeneric")
}

func TestRoot_helpExits0(t *testing.T) {
	r := run(t, []string{"--help"}, nil, nil)
	assert.Equal(t, 0, r.exit)
}
