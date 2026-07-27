package cmd

import (
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/data-pata/arat/internal/linear"
	"github.com/data-pata/arat/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- arat new --project / --in ---------------------------------------

func TestNew_projectFlag(t *testing.T) {
	svc := &fakeService{newResult: &workspace.Workspace{Name: "q3-billing", Ref: "q3-billing", Path: "/p", Kind: workspace.KindProject}}
	r := run(t, []string{"new", "q3-billing", "--project"}, nil, svc)

	assert.Equal(t, 0, r.exit, r.stderr)
	require.Len(t, svc.newCalls, 1)
	assert.Equal(t, workspace.KindProject, svc.newCalls[0].Kind)
	assert.Empty(t, svc.newCalls[0].Ticket)
	assert.Empty(t, svc.newCalls[0].Parent)
}

func TestNew_projectFlagRejectsTicket(t *testing.T) {
	for _, args := range [][]string{
		{"new", "q3-billing", "--project", "--ticket", "abc-1"},
		{"new", "q3-billing", "--project", "--new-ticket", "Some title"},
	} {
		t.Run(args[3], func(t *testing.T) {
			svc := &fakeService{}
			r := run(t, args, nil, svc)
			assert.Equal(t, ExitUsage, r.exit)
			assert.Contains(t, r.stderr, "--project cannot take a ticket")
			assert.Empty(t, svc.newCalls, "nothing should be created")
		})
	}
}

func TestNew_inFlagSetsParent(t *testing.T) {
	svc := &fakeService{
		getKnown: map[string]*workspace.Workspace{
			"q3-billing": {Name: "q3-billing", Ref: "q3-billing", Kind: workspace.KindProject},
		},
		newResult: &workspace.Workspace{Name: "abc-1--x", Ref: "q3-billing/abc-1--x", Path: "/p"},
	}
	r := run(t, []string{"new", "x", "--no-ticket", "--in", "q3-billing"}, nil, svc)

	assert.Equal(t, 0, r.exit, r.stderr)
	require.Len(t, svc.newCalls, 1)
	assert.Equal(t, "q3-billing", svc.newCalls[0].Parent)
}

func TestNew_inFlagRejectsTaskWorkspace(t *testing.T) {
	svc := &fakeService{
		getKnown: map[string]*workspace.Workspace{
			"abc-1--leaf": {Name: "abc-1--leaf", Ref: "abc-1--leaf", Kind: workspace.KindTask},
		},
	}
	r := run(t, []string{"new", "x", "--no-ticket", "--in", "abc-1--leaf"}, nil, svc)

	assert.Equal(t, ExitUsage, r.exit)
	assert.Contains(t, r.stderr, "not a project")
	assert.Empty(t, svc.newCalls)
}

func TestNew_inFlagUnknownProject(t *testing.T) {
	svc := &fakeService{getKnown: map[string]*workspace.Workspace{}}
	r := run(t, []string{"new", "x", "--no-ticket", "--in", "nope"}, nil, svc)

	assert.Equal(t, ExitUsage, r.exit)
	assert.Contains(t, r.stderr, "--in nope")
	assert.Empty(t, svc.newCalls)
}

func TestNew_parentInferredFromCwd(t *testing.T) {
	svc := &fakeService{
		projectAtRes: &workspace.Workspace{Name: "q3-billing", Ref: "q3-billing", Kind: workspace.KindProject},
		newResult:    &workspace.Workspace{Name: "abc-1--x", Ref: "q3-billing/abc-1--x", Path: "/p"},
	}
	cwdFn := func() (string, error) { return "/ws/feat/q3-billing/somewhere", nil }
	r := runWithDeps(t, []string{"new", "x", "--no-ticket"}, nil, svc, depsOpts{cwd: cwdFn})

	assert.Equal(t, 0, r.exit, r.stderr)
	require.Len(t, svc.newCalls, 1)
	assert.Equal(t, "q3-billing", svc.newCalls[0].Parent)
	assert.Equal(t, []string{"/ws/feat/q3-billing/somewhere"}, svc.projectAtCalls)
}

func TestNew_outsideAnyProjectStaysTopLevel(t *testing.T) {
	// ProjectAt reporting (nil, nil) means "not inside a project", which
	// must create a top-level workspace rather than fail.
	svc := &fakeService{newResult: &workspace.Workspace{Name: "x", Ref: "x", Path: "/p"}}
	cwdFn := func() (string, error) { return "/somewhere/else", nil }
	r := runWithDeps(t, []string{"new", "x", "--no-ticket"}, nil, svc, depsOpts{cwd: cwdFn})

	assert.Equal(t, 0, r.exit, r.stderr)
	require.Len(t, svc.newCalls, 1)
	assert.Empty(t, svc.newCalls[0].Parent)
}

func TestNew_projectSkipsInteractiveRepoPicker(t *testing.T) {
	svc := &fakeService{
		newResult:        &workspace.Workspace{Name: "q3-billing", Ref: "q3-billing", Path: "/p"},
		candidatesResult: []workspace.RepoCandidate{{Name: "repo-a", Selected: true}},
	}
	r := runWithDeps(t, []string{"new", "q3-billing", "--project"}, nil, svc, depsOpts{
		isTTY: func() bool { return true },
		repoFlow: func(context.Context, []workspace.RepoCandidate, io.Writer) (RepoFlowResult, error) {
			t.Fatal("the repo picker must not open for a project")
			return RepoFlowResult{}, nil
		},
	})
	assert.Equal(t, 0, r.exit, r.stderr)
	assert.Zero(t, svc.candidatesCalled)
}

// --- arat ls (tree) ---------------------------------------------------

func TestLs_tree(t *testing.T) {
	svc := &fakeService{listResult: []workspace.Workspace{
		{
			Name: "q3-billing", Ref: "q3-billing", Kind: workspace.KindProject,
			Linear: &workspace.LinearRef{Kind: "project", ID: "slug", Name: "Q3 Billing", URL: "https://linear.app/o/project/slug"},
			Repos:  []workspace.RepoStatus{{Name: "core-api", Branch: "ps--q3-billing"}},
			Children: []workspace.Workspace{
				{
					Name: "abc-12--invoice", Ref: "q3-billing/abc-12--invoice", Parent: "q3-billing",
					Kind:  workspace.KindTask,
					Repos: []workspace.RepoStatus{{Name: "core-api", Branch: "ps--invoice--abc-12", Dirty: true}},
				},
			},
		},
	}}
	r := run(t, []string{"ls"}, nil, svc)

	assert.Equal(t, 0, r.exit, r.stderr)
	assert.Contains(t, r.stdout, "── q3-billing ── (project)")
	assert.Contains(t, r.stdout, "linear project: Q3 Billing (https://linear.app/o/project/slug)")
	assert.Contains(t, r.stdout, "  core-api → ps--q3-billing")
	// The child is indented one level below its project.
	assert.Contains(t, r.stdout, "  ── abc-12--invoice ──")
	assert.Contains(t, r.stdout, "    core-api → ps--invoice--abc-12 *dirty*")
}

func TestLs_emptyProject(t *testing.T) {
	svc := &fakeService{listResult: []workspace.Workspace{
		{Name: "q3-billing", Ref: "q3-billing", Kind: workspace.KindProject},
	}}
	r := run(t, []string{"ls"}, nil, svc)

	assert.Equal(t, 0, r.exit, r.stderr)
	assert.Contains(t, r.stdout, "(no workspaces yet)")
	assert.NotContains(t, r.stdout, "(no worktrees)", "a project without worktrees is normal, not noteworthy")
}

// --- arat rm --recursive ----------------------------------------------

func TestRm_recursiveFlag(t *testing.T) {
	svc := &fakeService{}
	r := run(t, []string{"rm", "q3-billing", "--recursive"}, nil, svc)

	assert.Equal(t, 0, r.exit, r.stderr)
	require.Len(t, svc.removeCalls, 1)
	assert.True(t, svc.removeCalls[0].Recursive)
}

func TestRm_notEmptyPointsAtRecursiveNotForce(t *testing.T) {
	svc := &fakeService{removeErr: &workspace.ErrNotEmpty{
		Ref:      "q3-billing",
		Children: []string{"q3-billing/abc-12--invoice"},
	}}
	r := run(t, []string{"rm", "q3-billing"}, nil, svc)

	assert.Equal(t, ExitPrecondition, r.exit)
	assert.Contains(t, r.stderr, "run with --recursive")
	assert.NotContains(t, r.stderr, "--force", "--force does not clear a non-empty project")
}

func TestRm_pickerPromptNamesNestedCount(t *testing.T) {
	project := workspace.Workspace{
		Name: "q3-billing", Ref: "q3-billing", Path: "/p", Kind: workspace.KindProject,
		Children: []workspace.Workspace{
			{Name: "a", Ref: "q3-billing/a"},
			{Name: "b", Ref: "q3-billing/b"},
		},
	}
	svc := &fakeService{listResult: []workspace.Workspace{project}}
	var prompt string
	r := runWithDeps(t, []string{"rm"}, nil, svc, depsOpts{
		picker: func(context.Context, []workspace.Workspace, io.Writer) (*workspace.Workspace, error) {
			return &project, nil
		},
		confirm: func(p string) (bool, error) { prompt = p; return false, nil },
	})

	assert.Equal(t, 0, r.exit, r.stderr)
	assert.Contains(t, prompt, "Remove project \"q3-billing\" and the 2 workspaces nested in it?")
	assert.Empty(t, svc.removeCalls, "declining the prompt removes nothing")
}

// --- arat project link / unlink ---------------------------------------

func TestProjectLink_byName(t *testing.T) {
	svc := &fakeService{linkResult: &workspace.Workspace{
		Name: "q3-billing", Ref: "q3-billing", Path: "/p", Kind: workspace.KindProject,
	}}
	lc := &fakeLinear{available: true, containerResult: []linear.Container{
		{Kind: "project", ID: "slug-1", Name: "Q3 Billing", URL: "https://linear.app/o/project/slug-1"},
		{Kind: "project", ID: "slug-2", Name: "Something Else", URL: "https://linear.app/o/project/slug-2"},
	}}
	r := runWithDeps(t, []string{"project", "link", "q3-billing", "--project", "Q3 Billing"}, nil, svc, depsOpts{linear: lc})

	assert.Equal(t, 0, r.exit, r.stderr)
	assert.Equal(t, []string{"project"}, lc.containerCalls)
	require.Len(t, svc.linkCalls, 1)
	assert.Equal(t, "q3-billing", svc.linkCalls[0].Ref)
	assert.Equal(t, workspace.LinearRef{
		Kind: "project", ID: "slug-1", Name: "Q3 Billing", URL: "https://linear.app/o/project/slug-1",
	}, svc.linkCalls[0].Linear)
}

func TestProjectLink_bySlugIsCaseSensitiveAndWinsOverName(t *testing.T) {
	svc := &fakeService{linkResult: &workspace.Workspace{Name: "p", Ref: "p", Path: "/p", Kind: workspace.KindProject}}
	lc := &fakeLinear{available: true, containerResult: []linear.Container{
		{Kind: "initiative", ID: "abc123", Name: "Payments 2026", URL: "https://linear.app/o/initiative/abc123"},
	}}
	r := runWithDeps(t, []string{"project", "link", "p", "--initiative", "abc123"}, nil, svc, depsOpts{linear: lc})

	assert.Equal(t, 0, r.exit, r.stderr)
	assert.Equal(t, []string{"initiative"}, lc.containerCalls)
	require.Len(t, svc.linkCalls, 1)
	assert.Equal(t, "abc123", svc.linkCalls[0].Linear.ID)
}

func TestProjectLink_ambiguousName(t *testing.T) {
	svc := &fakeService{}
	lc := &fakeLinear{available: true, containerResult: []linear.Container{
		{Kind: "project", ID: "slug-1", Name: "Billing"},
		{Kind: "project", ID: "slug-2", Name: "billing"},
	}}
	r := runWithDeps(t, []string{"project", "link", "p", "--project", "Billing"}, nil, svc, depsOpts{linear: lc})

	assert.Equal(t, ExitNotFound, r.exit)
	assert.Contains(t, r.stderr, "matches 2 linear entries")
	assert.Empty(t, svc.linkCalls)
}

func TestProjectLink_noMatch(t *testing.T) {
	svc := &fakeService{}
	lc := &fakeLinear{available: true, containerResult: []linear.Container{
		{Kind: "project", ID: "slug-1", Name: "Billing"},
	}}
	r := runWithDeps(t, []string{"project", "link", "p", "--project", "Nope"}, nil, svc, depsOpts{linear: lc})

	assert.Equal(t, ExitNotFound, r.exit)
	assert.Contains(t, r.stderr, `no linear project matches "Nope"`)
	assert.Empty(t, svc.linkCalls)
}

func TestProjectLink_requiresExactlyOneTarget(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"neither", []string{"project", "link", "p"}, "one of --project or --initiative is required"},
		{"both", []string{"project", "link", "p", "--project", "a", "--initiative", "b"}, "mutually exclusive"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lc := &fakeLinear{available: true}
			r := runWithDeps(t, tc.args, nil, &fakeService{}, depsOpts{linear: lc})
			assert.Equal(t, ExitUsage, r.exit)
			assert.Contains(t, r.stderr, tc.want)
			assert.Empty(t, lc.containerCalls, "Linear must not be queried for an invalid invocation")
		})
	}
}

func TestProjectLink_rejectsTaskWorkspace(t *testing.T) {
	// The service is what enforces "projects only"; the command's job is to
	// surface that as a usage error rather than an external-tool failure.
	svc := &fakeService{linkErr: fmt.Errorf("%w: abc-1--leaf is a task workspace", workspace.ErrInvalidInput)}
	lc := &fakeLinear{available: true, containerResult: []linear.Container{
		{Kind: "project", ID: "slug-1", Name: "Billing"},
	}}
	r := runWithDeps(t, []string{"project", "link", "abc-1--leaf", "--project", "slug-1"}, nil, svc, depsOpts{linear: lc})

	assert.Equal(t, ExitUsage, r.exit)
	assert.Contains(t, r.stderr, "task workspace")
}

func TestProjectUnlink(t *testing.T) {
	svc := &fakeService{unlinkResult: &workspace.Workspace{Name: "q3-billing", Ref: "q3-billing", Path: "/p", Kind: workspace.KindProject}}
	r := run(t, []string{"project", "unlink", "q3-billing"}, nil, svc)

	assert.Equal(t, 0, r.exit, r.stderr)
	assert.Equal(t, []string{"q3-billing"}, svc.unlinkCalls)
	assert.Contains(t, r.stderr, "unlinked q3-billing")
}
