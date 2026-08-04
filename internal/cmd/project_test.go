package cmd

import (
	"context"
	"encoding/json"
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

	assert.Equal(t, ExitOK, r.exit, r.stderr)
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

	assert.Equal(t, ExitOK, r.exit, r.stderr)
	require.Len(t, svc.newCalls, 1)
	assert.Equal(t, "q3-billing", svc.newCalls[0].Parent)
}

// A task is a valid parent now: nesting one inside another is a sub-issue.
func TestNew_inFlagAcceptsTaskWorkspace(t *testing.T) {
	svc := &fakeService{
		getKnown: map[string]*workspace.Workspace{
			"abc-1--leaf": {Name: "abc-1--leaf", Ref: "abc-1--leaf", Kind: workspace.KindTask},
		},
		newResult: &workspace.Workspace{Name: "x", Ref: "abc-1--leaf/x", Path: "/p"},
	}
	r := run(t, []string{"new", "x", "--no-ticket", "--in", "abc-1--leaf"}, nil, svc)

	assert.Equal(t, ExitOK, r.exit, r.stderr)
	require.Len(t, svc.newCalls, 1)
	assert.Equal(t, "abc-1--leaf", svc.newCalls[0].Parent)
}

// "--in ." nests into the workspace at cwd, which is how a sub-issue of the
// task you are standing in is asked for. Plain `arat new` there would give a
// sibling instead.
func TestNew_inDotUsesWorkspaceAtCwd(t *testing.T) {
	svc := &fakeService{
		workspaceAtRes: &workspace.Workspace{Name: "abc-1--leaf", Ref: "q3/abc-1--leaf", Kind: workspace.KindTask},
		newResult:      &workspace.Workspace{Name: "x", Ref: "q3/abc-1--leaf/x", Path: "/p"},
	}
	cwdFn := func() (string, error) { return "/ws/q3/abc-1--leaf/repo-a", nil }
	r := runWithDeps(t, []string{"new", "x", "--no-ticket", "--in", "."}, nil, svc, depsOpts{cwd: cwdFn})

	assert.Equal(t, ExitOK, r.exit, r.stderr)
	require.Len(t, svc.newCalls, 1)
	assert.Equal(t, "q3/abc-1--leaf", svc.newCalls[0].Parent)
	assert.Empty(t, svc.projectAtCalls, "--in . must not fall back to project inference")
}

func TestNew_inDotOutsideAnyWorkspace(t *testing.T) {
	svc := &fakeService{}
	cwdFn := func() (string, error) { return "/somewhere/else", nil }
	r := runWithDeps(t, []string{"new", "x", "--no-ticket", "--in", "."}, nil, svc, depsOpts{cwd: cwdFn})

	assert.Equal(t, ExitUsage, r.exit)
	assert.Contains(t, r.stderr, "--in .")
	assert.Empty(t, svc.newCalls)
}

func TestNew_projectFlagRejectsIn(t *testing.T) {
	svc := &fakeService{}
	r := run(t, []string{"new", "q4", "--project", "--in", "q3-billing"}, nil, svc)

	assert.Equal(t, ExitUsage, r.exit)
	assert.Contains(t, r.stderr, "--project cannot be combined with --in")
	assert.Empty(t, svc.newCalls)
}

// Standing inside a project and creating another project must not try to nest
// it. Inference is skipped entirely rather than inferring and then failing.
func TestNew_projectIgnoresCwdInference(t *testing.T) {
	svc := &fakeService{
		projectAtRes: &workspace.Workspace{Name: "q3-billing", Ref: "q3-billing", Kind: workspace.KindProject},
		newResult:    &workspace.Workspace{Name: "q4", Ref: "q4", Path: "/p", Kind: workspace.KindProject},
	}
	cwdFn := func() (string, error) { return "/ws/q3-billing/somewhere", nil }
	r := runWithDeps(t, []string{"new", "q4", "--project"}, nil, svc, depsOpts{cwd: cwdFn})

	assert.Equal(t, ExitOK, r.exit, r.stderr)
	require.Len(t, svc.newCalls, 1)
	assert.Empty(t, svc.newCalls[0].Parent)
	assert.Empty(t, svc.projectAtCalls)
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

	assert.Equal(t, ExitOK, r.exit, r.stderr)
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

	assert.Equal(t, ExitOK, r.exit, r.stderr)
	require.Len(t, svc.newCalls, 1)
	assert.Empty(t, svc.newCalls[0].Parent)
}

func TestNew_nestingAloneDoesNotInheritBranches(t *testing.T) {
	svc := &fakeService{
		getKnown: map[string]*workspace.Workspace{
			"q3-billing": {Name: "q3-billing", Ref: "q3-billing", Kind: workspace.KindProject},
		},
		newResult: &workspace.Workspace{Name: "x", Ref: "q3-billing/x", Path: "/p"},
	}
	r := run(t, []string{"new", "x", "--no-ticket", "--in", "q3-billing"}, nil, svc)

	assert.Equal(t, ExitOK, r.exit, r.stderr)
	require.Len(t, svc.newCalls, 1)
	assert.False(t, svc.newCalls[0].InheritParentBranches)
}

func TestNew_fromParentSetsInheritance(t *testing.T) {
	svc := &fakeService{
		getKnown: map[string]*workspace.Workspace{
			"q3-billing": {Name: "q3-billing", Ref: "q3-billing", Kind: workspace.KindProject},
		},
		newResult: &workspace.Workspace{Name: "x", Ref: "q3-billing/x", Path: "/p"},
	}
	r := run(t, []string{"new", "x", "--no-ticket", "--in", "q3-billing", "--from-parent"}, nil, svc)

	assert.Equal(t, ExitOK, r.exit, r.stderr)
	require.Len(t, svc.newCalls, 1)
	assert.True(t, svc.newCalls[0].InheritParentBranches)
}

func TestNew_fromParentInfersParentFromCwd(t *testing.T) {
	svc := &fakeService{
		projectAtRes: &workspace.Workspace{Name: "q3-billing", Ref: "q3-billing", Kind: workspace.KindProject},
		newResult:    &workspace.Workspace{Name: "x", Ref: "q3-billing/x", Path: "/p"},
	}
	cwdFn := func() (string, error) { return "/ws/q3-billing/somewhere", nil }
	r := runWithDeps(t, []string{"new", "x", "--no-ticket", "--from-parent"}, nil, svc, depsOpts{cwd: cwdFn})

	assert.Equal(t, ExitOK, r.exit, r.stderr)
	require.Len(t, svc.newCalls, 1)
	assert.Equal(t, "q3-billing", svc.newCalls[0].Parent)
	assert.True(t, svc.newCalls[0].InheritParentBranches)
}

func TestNew_fromParentWithoutAParentIsUsageError(t *testing.T) {
	svc := &fakeService{newResult: &workspace.Workspace{Name: "x", Ref: "x", Path: "/p"}}
	cwdFn := func() (string, error) { return "/somewhere/else", nil }
	r := runWithDeps(t, []string{"new", "x", "--no-ticket", "--from-parent"}, nil, svc, depsOpts{cwd: cwdFn})

	assert.Equal(t, ExitUsage, r.exit)
	assert.Contains(t, r.stderr, "has no parent")
	assert.Empty(t, svc.newCalls)
}

func TestNew_fromParentConflictsWithFromCurrent(t *testing.T) {
	svc := &fakeService{}
	r := run(t, []string{"new", "x", "--no-ticket", "--from-parent", "--from-current"}, nil, svc)

	assert.Equal(t, ExitUsage, r.exit)
	assert.Contains(t, r.stderr, "mutually exclusive")
	assert.Empty(t, svc.newCalls)
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
	assert.Equal(t, ExitOK, r.exit, r.stderr)
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

	assert.Equal(t, ExitOK, r.exit, r.stderr)
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

	assert.Equal(t, ExitOK, r.exit, r.stderr)
	assert.Contains(t, r.stdout, "(no workspaces yet)")
	assert.NotContains(t, r.stdout, "(no worktrees)", "a project without worktrees is normal, not noteworthy")
}

func TestLs_flat(t *testing.T) {
	svc := &fakeService{listResult: []workspace.Workspace{
		{
			Name: "q3-billing", Ref: "q3-billing", Kind: workspace.KindProject,
			Repos: []workspace.RepoStatus{{Name: "core-api", Branch: "ps--q3-billing"}},
			Children: []workspace.Workspace{
				{
					Name: "abc-12--invoice", Ref: "q3-billing/abc-12--invoice", Parent: "q3-billing",
					Kind:  workspace.KindTask,
					Repos: []workspace.RepoStatus{{Name: "core-api", Branch: "ps--invoice--abc-12"}},
					Children: []workspace.Workspace{
						{
							Name: "abc-18--fonts", Ref: "q3-billing/abc-12--invoice/abc-18--fonts",
							Parent: "q3-billing/abc-12--invoice", Kind: workspace.KindTask,
						},
					},
				},
			},
		},
		{Name: "solo", Ref: "solo", Kind: workspace.KindTask},
	}}
	r := run(t, []string{"ls", "--flat"}, nil, svc)

	assert.Equal(t, ExitOK, r.exit, r.stderr)
	// Every workspace appears at column zero, headed by its full ref.
	assert.Contains(t, r.stdout, "── q3-billing ── (project)")
	assert.Contains(t, r.stdout, "\n── q3-billing/abc-12--invoice ──")
	assert.Contains(t, r.stdout, "\n── q3-billing/abc-12--invoice/abc-18--fonts ──")
	assert.Contains(t, r.stdout, "\n── solo ──")
	assert.NotContains(t, r.stdout, "  ── ", "flat output must not indent headers")
}

func TestLs_flatJSON(t *testing.T) {
	svc := &fakeService{listResult: []workspace.Workspace{
		{
			Name: "q3-billing", Ref: "q3-billing", Kind: workspace.KindProject,
			Children: []workspace.Workspace{
				{Name: "abc-12--invoice", Ref: "q3-billing/abc-12--invoice", Parent: "q3-billing", Kind: workspace.KindTask},
			},
		},
	}}
	r := run(t, []string{"ls", "--flat", "--json"}, nil, svc)

	assert.Equal(t, ExitOK, r.exit, r.stderr)
	var got []map[string]any
	require.NoError(t, json.Unmarshal([]byte(r.stdout), &got))
	require.Len(t, got, 2, "flat JSON lists every workspace exactly once")
	assert.Equal(t, "q3-billing", got[0]["ref"])
	assert.Equal(t, "q3-billing/abc-12--invoice", got[1]["ref"])
	for _, ws := range got {
		assert.NotContains(t, ws, "children", "children are stripped so no workspace appears twice")
	}
}

// --- arat repo add --recursive ----------------------------------------

func TestRepoAdd_recursiveFlagAndReporting(t *testing.T) {
	svc := &fakeService{addReposResult: &workspace.AddReposResult{
		Workspace: &workspace.Workspace{Name: "q3-billing", Ref: "q3-billing"},
		Outcomes: []workspace.WorkspaceAdd{
			{Ref: "q3-billing", Added: []workspace.RepoStatus{{Name: "ui-app", Path: "/q3/ui-app", Branch: "ps--q3-billing"}}},
			{Ref: "q3-billing/abc-1--x", Skipped: []string{"ui-app: already present"}},
			{Ref: "q3-billing/abc-2--y", Added: []workspace.RepoStatus{{Name: "ui-app", Path: "/q3/y/ui-app", Branch: "ps--y--abc-2"}}},
		},
	}}
	r := runWithDeps(t, []string{"repo", "add", "--workspace", "q3-billing", "--recursive", "ui-app"}, nil, svc, depsOpts{cwd: failingCwd(t)})

	assert.Equal(t, ExitOK, r.exit, r.stderr)
	require.Len(t, svc.addReposCalls, 1)
	assert.True(t, svc.addReposCalls[0].Recursive)
	// Every added worktree path lands on stdout for scripting.
	assert.Contains(t, r.stdout, "/q3/ui-app")
	assert.Contains(t, r.stdout, "/q3/y/ui-app")
	// Skips are reported per workspace on stderr.
	assert.Contains(t, r.stderr, "skipped q3-billing/abc-1--x: ui-app: already present")
}

// --- arat rm --recursive ----------------------------------------------

func TestRm_recursiveFlag(t *testing.T) {
	svc := &fakeService{}
	r := run(t, []string{"rm", "q3-billing", "--recursive"}, nil, svc)

	assert.Equal(t, ExitOK, r.exit, r.stderr)
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
		isTTY: func() bool { return true },
		picker: func(context.Context, []workspace.Workspace, io.Writer) (*workspace.Workspace, error) {
			return &project, nil
		},
		confirm: func(p string) (bool, error) { prompt = p; return false, nil },
	})

	assert.Equal(t, ExitOK, r.exit, r.stderr)
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

	assert.Equal(t, ExitOK, r.exit, r.stderr)
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

	assert.Equal(t, ExitOK, r.exit, r.stderr)
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

func TestProjectLink_interactivePicker(t *testing.T) {
	svc := &fakeService{linkResult: &workspace.Workspace{
		Name: "q3-billing", Ref: "q3-billing", Path: "/p", Kind: workspace.KindProject,
	}}
	lc := &fakeLinear{available: true, containerByKind: map[string][]linear.Container{
		"project": {
			{Kind: "project", ID: "slug-z", Name: "Zeta"},
			{Kind: "project", ID: "slug-a", Name: "Alpha"},
		},
		"initiative": {
			{Kind: "initiative", ID: "slug-i", Name: "Payments 2026"},
		},
	}}
	var offered []linear.Container
	pick := func(_ context.Context, containers []linear.Container, _ io.Writer) (*linear.Container, error) {
		offered = containers
		return &containers[2], nil
	}
	r := runWithDeps(t, []string{"project", "link", "q3-billing"}, nil, svc,
		depsOpts{linear: lc, isTTY: func() bool { return true }, pickContainer: pick})

	assert.Equal(t, ExitOK, r.exit, r.stderr)
	assert.Equal(t, []string{"project", "initiative"}, lc.containerCalls, "both kinds are offered")
	// Projects first (sorted), then initiatives.
	require.Len(t, offered, 3)
	assert.Equal(t, "Alpha", offered[0].Name)
	assert.Equal(t, "Zeta", offered[1].Name)
	assert.Equal(t, "Payments 2026", offered[2].Name)
	require.Len(t, svc.linkCalls, 1)
	assert.Equal(t, workspace.LinearRef{Kind: "initiative", ID: "slug-i", Name: "Payments 2026"}, svc.linkCalls[0].Linear)
}

func TestProjectLink_interactiveCancelled(t *testing.T) {
	lc := &fakeLinear{available: true, containerResult: []linear.Container{
		{Kind: "project", ID: "slug-1", Name: "Billing"},
	}}
	svc := &fakeService{}
	pick := func(_ context.Context, _ []linear.Container, _ io.Writer) (*linear.Container, error) {
		return nil, nil
	}
	r := runWithDeps(t, []string{"project", "link", "q3-billing"}, nil, svc,
		depsOpts{linear: lc, isTTY: func() bool { return true }, pickContainer: pick})

	assert.Equal(t, ExitUsage, r.exit)
	assert.Contains(t, r.stderr, "cancelled")
	assert.Empty(t, svc.linkCalls)
}

func TestProjectLink_interactiveNothingToPick(t *testing.T) {
	lc := &fakeLinear{available: true}
	pick := func(_ context.Context, _ []linear.Container, _ io.Writer) (*linear.Container, error) {
		t.Fatal("picker must not open on an empty list")
		return nil, nil
	}
	r := runWithDeps(t, []string{"project", "link", "q3-billing"}, nil, &fakeService{},
		depsOpts{linear: lc, isTTY: func() bool { return true }, pickContainer: pick})

	assert.Equal(t, ExitNotFound, r.exit)
	assert.Contains(t, r.stderr, "no linear projects or initiatives found")
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

	assert.Equal(t, ExitOK, r.exit, r.stderr)
	assert.Equal(t, []string{"q3-billing"}, svc.unlinkCalls)
	assert.Contains(t, r.stderr, "unlinked q3-billing")
}

// --- arat ls default vs --status --------------------------------------

func TestLs_defaultIsLight(t *testing.T) {
	svc := &fakeService{listResult: []workspace.Workspace{{Name: "x", Ref: "x"}}}
	r := run(t, []string{"ls"}, nil, svc)

	assert.Equal(t, ExitOK, r.exit, r.stderr)
	require.Len(t, svc.listCalls, 1)
	assert.Equal(t, workspace.DetailLight, svc.listCalls[0].Detail, "plain ls must use the no-git listing")
}

func TestLs_statusFlagUsesFullInspection(t *testing.T) {
	svc := &fakeService{listResult: []workspace.Workspace{{Name: "x", Ref: "x"}}}
	r := run(t, []string{"ls", "--status"}, nil, svc)

	assert.Equal(t, ExitOK, r.exit, r.stderr)
	require.Len(t, svc.listCalls, 1)
	assert.Equal(t, workspace.DetailFull, svc.listCalls[0].Detail)
}

// A wrong invocation must show what right looks like, not just name the
// mistake.
func TestUsageErrors_carryUsageLine(t *testing.T) {
	r := run(t, []string{"note"}, nil, &fakeService{})
	assert.Equal(t, ExitUsage, r.exit)
	assert.Contains(t, r.stderr, "usage: arat note [name] <text...>")
	assert.Contains(t, r.stderr, "see 'arat note --help'")

	r = run(t, []string{"ls", "--bogus"}, nil, &fakeService{})
	assert.Equal(t, ExitUsage, r.exit)
	assert.Contains(t, r.stderr, "unknown flag")
	assert.Contains(t, r.stderr, "usage: arat ls")
}

// --- cwd inference for link / unlink / ticket attach -------------------

func TestProjectLink_refInferredFromCwd(t *testing.T) {
	svc := &fakeService{
		projectAtRes: &workspace.Workspace{Name: "lidl", Ref: "lidl", Kind: workspace.KindProject},
		linkResult:   &workspace.Workspace{Name: "lidl", Ref: "lidl", Path: "/p", Kind: workspace.KindProject},
	}
	lc := &fakeLinear{available: true, containerResult: []linear.Container{
		{Kind: "project", ID: "slug-1", Name: "Lidl in Offers"},
	}}
	cwdFn := func() (string, error) { return "/ws/lidl/somewhere", nil }
	r := runWithDeps(t, []string{"project", "link", "--project", "slug-1"}, nil, svc,
		depsOpts{linear: lc, cwd: cwdFn})

	assert.Equal(t, ExitOK, r.exit, r.stderr)
	require.Len(t, svc.linkCalls, 1)
	assert.Equal(t, "lidl", svc.linkCalls[0].Ref)
	assert.Equal(t, []string{"/ws/lidl/somewhere"}, svc.projectAtCalls)
}

func TestProjectLink_noRefOutsideProject(t *testing.T) {
	svc := &fakeService{}
	lc := &fakeLinear{available: true, containerResult: []linear.Container{
		{Kind: "project", ID: "slug-1", Name: "Billing"},
	}}
	cwdFn := func() (string, error) { return "/somewhere/else", nil }
	r := runWithDeps(t, []string{"project", "link", "--project", "slug-1"}, nil, svc,
		depsOpts{linear: lc, cwd: cwdFn})

	assert.Equal(t, ExitUsage, r.exit)
	assert.Contains(t, r.stderr, "pass the ref explicitly")
	assert.Empty(t, svc.linkCalls)
}

func TestProjectUnlink_refInferredFromCwd(t *testing.T) {
	svc := &fakeService{
		projectAtRes: &workspace.Workspace{Name: "lidl", Ref: "lidl", Kind: workspace.KindProject},
		unlinkResult: &workspace.Workspace{Name: "lidl", Ref: "lidl", Path: "/p", Kind: workspace.KindProject},
	}
	cwdFn := func() (string, error) { return "/ws/lidl", nil }
	r := runWithDeps(t, []string{"project", "unlink"}, nil, svc, depsOpts{cwd: cwdFn})

	assert.Equal(t, ExitOK, r.exit, r.stderr)
	assert.Equal(t, []string{"lidl"}, svc.unlinkCalls)
}

func TestTicketAttach_workspaceInferredFromCwd(t *testing.T) {
	svc := &fakeService{
		workspaceAtRes: &workspace.Workspace{Name: "myfeat", Ref: "q3/myfeat"},
		attachResult: &workspace.AttachResult{
			Workspace: &workspace.Workspace{Name: "abc-1--myfeat", Ref: "q3/abc-1--myfeat", Path: "/p"},
		},
	}
	cwdFn := func() (string, error) { return "/ws/q3/myfeat/repo-a", nil }
	r := runWithDeps(t, []string{"ticket", "attach", "ABC-1"}, nil, svc, depsOpts{cwd: cwdFn})

	assert.Equal(t, ExitOK, r.exit, r.stderr)
	require.Len(t, svc.attachCalls, 1)
	assert.Equal(t, "q3/myfeat", svc.attachCalls[0].Name)
	assert.Equal(t, "abc-1", svc.attachCalls[0].Ticket)
}

func TestTicketAttach_singleArgOutsideWorkspace(t *testing.T) {
	svc := &fakeService{}
	cwdFn := func() (string, error) { return "/somewhere/else", nil }
	r := runWithDeps(t, []string{"ticket", "attach", "abc-1"}, nil, svc, depsOpts{cwd: cwdFn})

	assert.Equal(t, ExitUsage, r.exit)
	assert.Contains(t, r.stderr, "pass the ref explicitly")
	assert.Empty(t, svc.attachCalls)
}
