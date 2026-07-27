package cmd

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/data-pata/arat/internal/config"
	"github.com/data-pata/arat/internal/linear"
	"github.com/data-pata/arat/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func attachTaskWS() *workspace.Workspace {
	return &workspace.Workspace{
		Name: "my-feat", Ref: "my-feat", Path: "/ws/my-feat",
		Kind: workspace.KindTask, ShortName: "my-feat",
	}
}

func attachProjectWS() *workspace.Workspace {
	return &workspace.Workspace{
		Name: "q3", Ref: "q3", Path: "/ws/q3", Kind: workspace.KindProject,
	}
}

func attachedResult() *workspace.AttachResult {
	return &workspace.AttachResult{Workspace: &workspace.Workspace{
		Name: "abc-123--my-feat", Ref: "abc-123--my-feat", Path: "/ws/abc-123--my-feat",
		Kind: workspace.KindTask, ShortName: "my-feat", Ticket: "abc-123",
	}}
}

func cwdIn(dir string) func() (string, error) {
	return func() (string, error) { return dir, nil }
}

// --- attach: task workspaces -----------------------------------------

func TestAttach_taskTicketFromCwd(t *testing.T) {
	svc := &fakeService{workspaceAtRes: attachTaskWS(), attachResult: attachedResult()}
	r := runWithDeps(t, []string{"attach", "abc-123"}, nil, svc, depsOpts{cwd: cwdIn("/ws/my-feat")})
	assert.Equal(t, 0, r.exit, r.stderr)
	require.Len(t, svc.attachCalls, 1)
	assert.Equal(t, "my-feat", svc.attachCalls[0].Name)
	assert.Equal(t, "abc-123", svc.attachCalls[0].Ticket)
	assert.Contains(t, r.stdout, "/ws/abc-123--my-feat")
	assert.Contains(t, r.stderr, "attached issue ABC-123 → abc-123--my-feat")
}

func TestAttach_taskExplicitRefAndTicket(t *testing.T) {
	svc := &fakeService{
		getKnown:     map[string]*workspace.Workspace{"my-feat": attachTaskWS()},
		attachResult: attachedResult(),
	}
	r := runWithDeps(t, []string{"attach", "my-feat", "ABC-123"}, nil, svc, depsOpts{})
	assert.Equal(t, 0, r.exit, r.stderr)
	require.Len(t, svc.attachCalls, 1)
	assert.Equal(t, "my-feat", svc.attachCalls[0].Name)
	assert.Equal(t, "abc-123", svc.attachCalls[0].Ticket, "ticket is lowercased before the domain call")
}

func TestAttach_taskJSON(t *testing.T) {
	svc := &fakeService{workspaceAtRes: attachTaskWS(), attachResult: attachedResult()}
	r := runWithDeps(t, []string{"attach", "abc-123", "--json"}, nil, svc, depsOpts{cwd: cwdIn("/ws/my-feat")})
	assert.Equal(t, 0, r.exit, r.stderr)
	var got workspace.Workspace
	require.NoError(t, json.Unmarshal([]byte(r.stdout), &got))
	assert.Equal(t, "abc-123--my-feat", got.Ref)
	assert.Contains(t, r.stderr, "attached issue ABC-123", "confirmation must not be swallowed by --json")
}

func TestAttach_taskNew(t *testing.T) {
	svc := &fakeService{workspaceAtRes: attachTaskWS(), attachResult: attachedResult()}
	lc := &fakeLinear{available: true, createResult: linear.IssueResult{ID: "ABC-123"}}
	r := runWithDeps(t, []string{"attach", "--new", "Fix the race", "-d", "body"}, nil, svc,
		depsOpts{cwd: cwdIn("/ws/my-feat"), linear: lc})
	assert.Equal(t, 0, r.exit, r.stderr)
	require.Len(t, lc.createCalls, 1)
	assert.Equal(t, "Fix the race", lc.createCalls[0].Title)
	assert.Equal(t, "body", lc.createCalls[0].Description)
	assert.Equal(t, "ABC", lc.createCalls[0].Team, "falls back to linear.default_team")
	assert.Equal(t, "Backlog", lc.createCalls[0].State)
	require.Len(t, svc.attachCalls, 1)
	assert.Equal(t, "abc-123", svc.attachCalls[0].Ticket)
}

func TestAttach_taskNewWithExplicitRef(t *testing.T) {
	svc := &fakeService{
		getKnown:     map[string]*workspace.Workspace{"my-feat": attachTaskWS()},
		attachResult: attachedResult(),
	}
	lc := &fakeLinear{available: true, createResult: linear.IssueResult{ID: "ABC-123"}}
	r := runWithDeps(t, []string{"attach", "my-feat", "--new", "Fix the race"}, nil, svc, depsOpts{linear: lc})
	assert.Equal(t, 0, r.exit, r.stderr)
	assert.Contains(t, svc.getCalls, "my-feat", "a lone positional under --new is the workspace ref")
	require.Len(t, svc.attachCalls, 1)
}

func TestAttach_taskNoArgNonTTY(t *testing.T) {
	svc := &fakeService{workspaceAtRes: attachTaskWS()}
	r := runWithDeps(t, []string{"attach"}, nil, svc, depsOpts{cwd: cwdIn("/ws/my-feat")})
	assert.Equal(t, ExitUsage, r.exit)
	assert.Contains(t, r.stderr, "outside a terminal")
	assert.Contains(t, r.stderr, "--new")
	assert.Empty(t, svc.attachCalls)
}

func TestAttach_taskInteractiveFlowPick(t *testing.T) {
	svc := &fakeService{workspaceAtRes: attachTaskWS(), attachResult: attachedResult()}
	lc := &fakeLinear{available: true}
	flow := func(_ context.Context, _ linear.Reader, opts TicketFlowOptions, _ io.Writer) (TicketFlowResult, error) {
		assert.False(t, opts.AllowSkip, "attach has nothing to skip to")
		assert.Equal(t, "ABC", opts.Team)
		return TicketFlowResult{Ticket: "ABC-7"}, nil
	}
	r := runWithDeps(t, []string{"attach"}, nil, svc, depsOpts{
		cwd: cwdIn("/ws/my-feat"), linear: lc, tickFlow: flow, isTTY: func() bool { return true },
	})
	assert.Equal(t, 0, r.exit, r.stderr)
	require.Len(t, svc.attachCalls, 1)
	assert.Equal(t, "abc-7", svc.attachCalls[0].Ticket)
}

func TestAttach_taskInteractiveFlowCreate(t *testing.T) {
	svc := &fakeService{workspaceAtRes: attachTaskWS(), attachResult: attachedResult()}
	lc := &fakeLinear{available: true, createResult: linear.IssueResult{ID: "ABC-123"}}
	flow := func(_ context.Context, _ linear.Reader, _ TicketFlowOptions, _ io.Writer) (TicketFlowResult, error) {
		return TicketFlowResult{NewTitle: "Typed inline", NewDescription: "desc"}, nil
	}
	r := runWithDeps(t, []string{"attach"}, nil, svc, depsOpts{
		cwd: cwdIn("/ws/my-feat"), linear: lc, tickFlow: flow, isTTY: func() bool { return true },
	})
	assert.Equal(t, 0, r.exit, r.stderr)
	require.Len(t, lc.createCalls, 1)
	assert.Equal(t, "Typed inline", lc.createCalls[0].Title)
	require.Len(t, svc.attachCalls, 1)
	assert.Equal(t, "abc-123", svc.attachCalls[0].Ticket)
}

func TestAttach_taskInteractiveFlowCancelled(t *testing.T) {
	svc := &fakeService{workspaceAtRes: attachTaskWS()}
	lc := &fakeLinear{available: true}
	flow := func(_ context.Context, _ linear.Reader, _ TicketFlowOptions, _ io.Writer) (TicketFlowResult, error) {
		return TicketFlowResult{Cancelled: true}, nil
	}
	r := runWithDeps(t, []string{"attach"}, nil, svc, depsOpts{
		cwd: cwdIn("/ws/my-feat"), linear: lc, tickFlow: flow, isTTY: func() bool { return true },
	})
	assert.Equal(t, ExitUsage, r.exit)
	assert.Contains(t, r.stderr, "cancelled")
	assert.Empty(t, svc.attachCalls)
}

// --- attach: project workspaces --------------------------------------

func TestAttach_projectByName(t *testing.T) {
	svc := &fakeService{
		getKnown:   map[string]*workspace.Workspace{"q3": attachProjectWS()},
		linkResult: attachProjectWS(),
	}
	lc := &fakeLinear{available: true, containerByKind: map[string][]linear.Container{
		linear.ContainerProject:    {{Kind: "project", ID: "slug1", Name: "Q3 Billing", URL: "https://l/p/slug1"}},
		linear.ContainerInitiative: {{Kind: "initiative", ID: "slug2", Name: "Payments", URL: "https://l/i/slug2"}},
	}}
	r := runWithDeps(t, []string{"attach", "q3", "q3 billing"}, nil, svc, depsOpts{linear: lc})
	assert.Equal(t, 0, r.exit, r.stderr)
	assert.ElementsMatch(t, []string{"project", "initiative"}, lc.containerCalls, "both kinds are searched")
	require.Len(t, svc.linkCalls, 1)
	assert.Equal(t, "q3", svc.linkCalls[0].Ref)
	assert.Equal(t, "slug1", svc.linkCalls[0].Linear.ID)
	assert.Contains(t, r.stderr, `linked q3 → project "Q3 Billing"`)
}

func TestAttach_projectInitiativeBySlug(t *testing.T) {
	svc := &fakeService{
		getKnown:   map[string]*workspace.Workspace{"q3": attachProjectWS()},
		linkResult: attachProjectWS(),
	}
	lc := &fakeLinear{available: true, containerByKind: map[string][]linear.Container{
		linear.ContainerProject:    {{Kind: "project", ID: "slug1", Name: "Q3 Billing"}},
		linear.ContainerInitiative: {{Kind: "initiative", ID: "slug2", Name: "Payments"}},
	}}
	r := runWithDeps(t, []string{"attach", "q3", "slug2"}, nil, svc, depsOpts{linear: lc})
	assert.Equal(t, 0, r.exit, r.stderr)
	require.Len(t, svc.linkCalls, 1)
	assert.Equal(t, "initiative", svc.linkCalls[0].Linear.Kind)
}

func TestAttach_projectCrossKindNameAmbiguity(t *testing.T) {
	svc := &fakeService{getKnown: map[string]*workspace.Workspace{"q3": attachProjectWS()}}
	lc := &fakeLinear{available: true, containerByKind: map[string][]linear.Container{
		linear.ContainerProject:    {{Kind: "project", ID: "slug1", Name: "Billing"}},
		linear.ContainerInitiative: {{Kind: "initiative", ID: "slug2", Name: "Billing"}},
	}}
	r := runWithDeps(t, []string{"attach", "q3", "Billing"}, nil, svc, depsOpts{linear: lc})
	assert.Equal(t, ExitNotFound, r.exit)
	assert.Contains(t, r.stderr, "slug1")
	assert.Contains(t, r.stderr, "slug2")
	assert.Contains(t, r.stderr, "slug id to disambiguate")
	assert.Empty(t, svc.linkCalls)
}

func TestAttach_projectNotFoundNamesBothKinds(t *testing.T) {
	svc := &fakeService{getKnown: map[string]*workspace.Workspace{"q3": attachProjectWS()}}
	lc := &fakeLinear{available: true, containerByKind: map[string][]linear.Container{
		linear.ContainerProject: {{Kind: "project", ID: "slug1", Name: "Billing"}},
	}}
	r := runWithDeps(t, []string{"attach", "q3", "nope"}, nil, svc, depsOpts{linear: lc})
	assert.Equal(t, ExitNotFound, r.exit)
	assert.Contains(t, r.stderr, "no linear project or initiative matches")
}

func TestAttach_projectNew(t *testing.T) {
	svc := &fakeService{
		workspaceAtRes: attachProjectWS(),
		linkResult:     attachProjectWS(),
	}
	lc := &fakeLinear{available: true, projectCreateResult: linear.Container{
		Kind: "project", ID: "slugN", Name: "New Proj", URL: "https://l/p/slugN",
	}}
	r := runWithDeps(t, []string{"attach", "--new", "New Proj", "-d", "why"}, nil, svc,
		depsOpts{cwd: cwdIn("/ws/q3"), linear: lc})
	assert.Equal(t, 0, r.exit, r.stderr)
	require.Len(t, lc.projectCreateCalls, 1)
	assert.Equal(t, "New Proj", lc.projectCreateCalls[0].Name)
	assert.Equal(t, "ABC", lc.projectCreateCalls[0].Team)
	assert.Equal(t, "why", lc.projectCreateCalls[0].Description)
	require.Len(t, svc.linkCalls, 1)
	assert.Equal(t, "slugN", svc.linkCalls[0].Linear.ID)
	assert.Contains(t, r.stderr, `created linear project "New Proj"`)
	assert.Contains(t, r.stderr, "linked q3")
}

func TestAttach_projectNewWithoutDefaultTeam(t *testing.T) {
	cfg := &config.Config{Root: "/tmp", BranchPrefix: "ps", Linear: config.LinearConfig{Enabled: true}}
	svc := &fakeService{workspaceAtRes: attachProjectWS()}
	lc := &fakeLinear{available: true}
	r := runWithDeps(t, []string{"attach", "--new", "New Proj"}, cfg, svc,
		depsOpts{cwd: cwdIn("/ws/q3"), linear: lc})
	assert.Equal(t, ExitUsage, r.exit)
	assert.Contains(t, r.stderr, "default_team")
	assert.Empty(t, lc.projectCreateCalls)
}

func TestAttach_projectInteractivePick(t *testing.T) {
	svc := &fakeService{workspaceAtRes: attachProjectWS(), linkResult: attachProjectWS()}
	lc := &fakeLinear{available: true, containerByKind: map[string][]linear.Container{
		linear.ContainerProject: {{Kind: "project", ID: "slug1", Name: "Q3 Billing", URL: "u"}},
	}}
	picker := func(_ context.Context, containers []linear.Container, _ io.Writer) (*linear.Container, error) {
		require.NotEmpty(t, containers)
		return &containers[0], nil
	}
	r := runWithDeps(t, []string{"attach"}, nil, svc, depsOpts{
		cwd: cwdIn("/ws/q3"), linear: lc, pickContainer: picker, isTTY: func() bool { return true },
	})
	assert.Equal(t, 0, r.exit, r.stderr)
	require.Len(t, svc.linkCalls, 1)
	assert.Equal(t, "slug1", svc.linkCalls[0].Linear.ID)
}

func TestAttach_projectNoArgNonTTY(t *testing.T) {
	// Usage-error precedence: with nothing named and no terminal, the exit is
	// a stable 2 before any Linear access — even NewLinear is left unwired.
	svc := &fakeService{workspaceAtRes: attachProjectWS()}
	r := runWithDeps(t, []string{"attach"}, nil, svc, depsOpts{cwd: cwdIn("/ws/q3")})
	assert.Equal(t, ExitUsage, r.exit)
	assert.Contains(t, r.stderr, "project workspace")
	assert.Contains(t, r.stderr, "--new")
}

// --- attach: argument validation --------------------------------------

func TestAttach_newWithTwoPositionals(t *testing.T) {
	r := runWithDeps(t, []string{"attach", "ref", "abc-123", "--new", "T"}, nil, nil, depsOpts{})
	assert.Equal(t, ExitUsage, r.exit)
	assert.Contains(t, r.stderr, "--new replaces")
}

func TestAttach_descriptionRequiresNew(t *testing.T) {
	r := runWithDeps(t, []string{"attach", "abc-123", "-d", "body"}, nil, nil, depsOpts{})
	assert.Equal(t, ExitUsage, r.exit)
	assert.Contains(t, r.stderr, "--description requires --new")
}

func TestAttach_emptyNew(t *testing.T) {
	r := runWithDeps(t, []string{"attach", "--new", "  "}, nil, nil, depsOpts{})
	assert.Equal(t, ExitUsage, r.exit)
	assert.Contains(t, r.stderr, "non-empty")
}

func TestAttach_unknownRef(t *testing.T) {
	svc := &fakeService{getKnown: map[string]*workspace.Workspace{}}
	r := runWithDeps(t, []string{"attach", "nope", "abc-123"}, nil, svc, depsOpts{})
	assert.Equal(t, ExitNotFound, r.exit)
}

func TestAttach_noCwdWorkspace(t *testing.T) {
	svc := &fakeService{} // WorkspaceAt reports "not inside a workspace"
	r := runWithDeps(t, []string{"attach", "abc-123"}, nil, svc, depsOpts{cwd: cwdIn("/elsewhere")})
	assert.Equal(t, ExitUsage, r.exit)
	assert.Contains(t, r.stderr, "pass the ref explicitly")
}

// --- detach -----------------------------------------------------------

func TestDetach_project(t *testing.T) {
	svc := &fakeService{
		getKnown:     map[string]*workspace.Workspace{"q3": attachProjectWS()},
		unlinkResult: attachProjectWS(),
	}
	r := runWithDeps(t, []string{"detach", "q3"}, nil, svc, depsOpts{})
	assert.Equal(t, 0, r.exit, r.stderr)
	assert.Equal(t, []string{"q3"}, svc.unlinkCalls)
	assert.Contains(t, r.stdout, "/ws/q3")
	assert.Contains(t, r.stderr, "unlinked q3")
}

func TestDetach_projectFromCwd(t *testing.T) {
	svc := &fakeService{workspaceAtRes: attachProjectWS(), unlinkResult: attachProjectWS()}
	r := runWithDeps(t, []string{"detach"}, nil, svc, depsOpts{cwd: cwdIn("/ws/q3")})
	assert.Equal(t, 0, r.exit, r.stderr)
	assert.Equal(t, []string{"q3"}, svc.unlinkCalls)
}

func TestDetach_taskWithTicketRefuses(t *testing.T) {
	ws := attachTaskWS()
	ws.Ticket = "abc-12"
	svc := &fakeService{workspaceAtRes: ws}
	r := runWithDeps(t, []string{"detach"}, nil, svc, depsOpts{cwd: cwdIn("/ws/my-feat")})
	assert.Equal(t, ExitUsage, r.exit)
	assert.Contains(t, r.stderr, "cannot detach ticket ABC-12")
	assert.Contains(t, r.stderr, "not supported")
	assert.Empty(t, svc.unlinkCalls)
}

func TestDetach_taskWithoutTicketIsNoop(t *testing.T) {
	svc := &fakeService{workspaceAtRes: attachTaskWS()}
	r := runWithDeps(t, []string{"detach"}, nil, svc, depsOpts{cwd: cwdIn("/ws/my-feat")})
	assert.Equal(t, 0, r.exit, r.stderr)
	assert.Contains(t, r.stderr, "nothing to detach")
	assert.Empty(t, svc.unlinkCalls)
}
