package workspace

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gitOutput runs a read-only git command in dir and returns its output.
func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	require.NoErrorf(t, err, "git %v: %s", args, out)
	return string(out)
}

func TestNew_project_hasNoWorktreesByDefault(t *testing.T) {
	root := setupRoot(t, "repo-a", "repo-b")
	svc := newSvc(t, root)

	ws, err := svc.New(t.Context(), NewOptions{ShortName: "q3-billing", Kind: KindProject})
	require.NoError(t, err)

	assert.Equal(t, "q3-billing", ws.Name)
	assert.Equal(t, "q3-billing", ws.Ref)
	assert.Equal(t, KindProject, ws.Kind)
	assert.Empty(t, ws.Repos, "a project groups work; worktrees are opt-in via --repos")

	// The directory carries the marker and the shared context file.
	assert.FileExists(t, filepath.Join(ws.Path, MetaFile))
	body, err := os.ReadFile(filepath.Join(ws.Path, "CLAUDE.md"))
	require.NoError(t, err)
	assert.Contains(t, string(body), "Project workspace")
	assert.Contains(t, string(body), "groups workspaces only")
}

func TestNew_project_rejectsTicket(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)

	_, err := svc.New(t.Context(), NewOptions{ShortName: "q3-billing", Kind: KindProject, Ticket: "abc-1"})
	assert.ErrorIs(t, err, ErrInvalidInput)
}

// projectWithCommit creates a project carrying a worktree of repo-a on its own
// branch, with one commit that origin/HEAD does not have. The returned marker
// file is present in a worktree exactly when it branched off the project.
func projectWithCommit(t *testing.T, svc *Service, ctx context.Context) (*Workspace, string) {
	t.Helper()
	proj, err := svc.New(ctx, NewOptions{
		ShortName: "q3-billing",
		Kind:      KindProject,
		Repos:     []string{"repo-a"},
	})
	require.NoError(t, err)
	require.Len(t, proj.Repos, 1)
	require.Equal(t, "ps--q3-billing", proj.Repos[0].Branch)

	wt := filepath.Join(proj.Path, "repo-a")
	require.NoError(t, os.WriteFile(filepath.Join(wt, "project-only.txt"), []byte("x"), 0o644))
	runGit(t, wt, "add", ".")
	runGit(t, wt, "commit", "-m", "project scaffolding")
	return proj, "project-only.txt"
}

func TestNew_insideProject_nestsButKeepsDefaultBase(t *testing.T) {
	root := setupRoot(t, "repo-a", "repo-b")
	svc := newSvc(t, root)
	ctx := t.Context()

	proj, marker := projectWithCommit(t, svc, ctx)

	child, err := svc.New(ctx, NewOptions{
		ShortName: "invoice-pdf",
		Ticket:    "abc-12",
		Repos:     []string{"repo-a"},
		Parent:    "q3-billing",
	})
	require.NoError(t, err)

	assert.Equal(t, "abc-12--invoice-pdf", child.Name)
	assert.Equal(t, "q3-billing/abc-12--invoice-pdf", child.Ref)
	assert.Equal(t, "q3-billing", child.Parent)
	assert.Equal(t, filepath.Join(proj.Path, "abc-12--invoice-pdf"), child.Path)

	// Nesting alone does not inherit: the child started from the default
	// base, so the project-only commit is absent.
	assert.NoFileExists(t, filepath.Join(child.Path, "repo-a", marker))

	// The tree reflects the nesting.
	items, err := svc.List(ctx, ListOptions{})
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Len(t, items[0].Children, 1)
	assert.Equal(t, "q3-billing/abc-12--invoice-pdf", items[0].Children[0].Ref)
}

func TestNew_insideProject_inheritsBaseWhenAsked(t *testing.T) {
	root := setupRoot(t, "repo-a", "repo-b")
	svc := newSvc(t, root)
	ctx := t.Context()

	_, marker := projectWithCommit(t, svc, ctx)

	child, err := svc.New(ctx, NewOptions{
		ShortName:             "invoice-pdf",
		Ticket:                "abc-12",
		Repos:                 []string{"repo-a"},
		Parent:                "q3-billing",
		InheritParentBranches: true,
	})
	require.NoError(t, err)

	// Opted in, so the child branched off the project's branch and carries
	// the project-only commit.
	assert.FileExists(t, filepath.Join(child.Path, "repo-a", marker))
}

func TestNew_insideProject_explicitBaseBeatsInheritance(t *testing.T) {
	root := setupRoot(t, "repo-a", "repo-b")
	svc := newSvc(t, root)
	ctx := t.Context()

	_, marker := projectWithCommit(t, svc, ctx)

	child, err := svc.New(ctx, NewOptions{
		ShortName:             "invoice-pdf",
		Repos:                 []string{"repo-a"},
		Parent:                "q3-billing",
		InheritParentBranches: true,
		BaseByRepo:            map[string]string{"repo-a": "main"},
	})
	require.NoError(t, err)
	assert.NoFileExists(t, filepath.Join(child.Path, "repo-a", marker))
}

func TestNew_inheritWithoutParentIsInvalid(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)

	_, err := svc.New(t.Context(), NewOptions{
		ShortName:             "loose",
		Repos:                 []string{"repo-a"},
		InheritParentBranches: true,
	})
	assert.ErrorIs(t, err, ErrInvalidInput)
}

func TestNew_insideProject_repoNotInProjectUsesDefaultBase(t *testing.T) {
	root := setupRoot(t, "repo-a", "repo-b")
	svc := newSvc(t, root)
	ctx := t.Context()

	_, err := svc.New(ctx, NewOptions{ShortName: "q3-billing", Kind: KindProject, Repos: []string{"repo-a"}})
	require.NoError(t, err)

	// repo-b has no counterpart in the project, so it falls back to Base
	// even though inheritance was requested.
	child, err := svc.New(ctx, NewOptions{
		ShortName:             "wider",
		Repos:                 []string{"repo-a", "repo-b"},
		Parent:                "q3-billing",
		InheritParentBranches: true,
	})
	require.NoError(t, err)
	assert.Len(t, child.Repos, 2)
}

// A task in a task is a sub-issue, so a task workspace is a valid parent and
// shows up in the tree exactly like a project's child does.
func TestNew_taskHoldsSubTasks(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)
	ctx := t.Context()

	parent, err := svc.New(ctx, NewOptions{ShortName: "invoice-pdf", Ticket: "abc-12", Repos: []string{"repo-a"}})
	require.NoError(t, err)

	child, err := svc.New(ctx, NewOptions{
		ShortName: "fonts",
		Ticket:    "abc-18",
		Repos:     []string{"repo-a"},
		Parent:    "abc-12--invoice-pdf",
	})
	require.NoError(t, err)
	assert.Equal(t, "abc-12--invoice-pdf/abc-18--fonts", child.Ref)
	assert.Equal(t, "abc-12--invoice-pdf", child.Parent)
	assert.Equal(t, filepath.Join(parent.Path, "abc-18--fonts"), child.Path)

	// The parent still reports its own worktree, and now also its child.
	got, err := svc.Get(ctx, "abc-12--invoice-pdf")
	require.NoError(t, err)
	assert.Equal(t, KindTask, got.Kind)
	require.Len(t, got.Repos, 1)
	assert.Equal(t, "repo-a", got.Repos[0].Name)
	require.Len(t, got.Children, 1)
	assert.Equal(t, "abc-12--invoice-pdf/abc-18--fonts", got.Children[0].Ref)

	// A sub-issue of a sub-issue is fine too.
	grand, err := svc.New(ctx, NewOptions{
		ShortName: "kerning",
		Repos:     []string{"repo-a"},
		Parent:    "abc-12--invoice-pdf/abc-18--fonts",
	})
	require.NoError(t, err)
	assert.Equal(t, "abc-12--invoice-pdf/abc-18--fonts/kerning", grand.Ref)
}

// Linear has no project inside a project or inside an issue, so neither does
// arat.
func TestNew_projectCannotBeNested(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)
	ctx := t.Context()

	_, err := svc.New(ctx, NewOptions{ShortName: "q3-billing", Kind: KindProject})
	require.NoError(t, err)
	_, err = svc.New(ctx, NewOptions{ShortName: "leaf", Repos: []string{"repo-a"}})
	require.NoError(t, err)

	for _, parent := range []string{"q3-billing", "leaf"} {
		t.Run(parent, func(t *testing.T) {
			_, err := svc.New(ctx, NewOptions{ShortName: "nested", Kind: KindProject, Parent: parent})
			assert.ErrorIs(t, err, ErrInvalidInput)
			assert.NoDirExists(t, filepath.Join(svc.WorkspacesDir, parent, "nested"))
		})
	}
}

// Inheriting from a task parent is the sub-issue equivalent of stacking on a
// project's integration branch.
func TestNew_subTaskInheritsParentBranchWhenAsked(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)
	ctx := t.Context()

	parent, err := svc.New(ctx, NewOptions{ShortName: "invoice-pdf", Repos: []string{"repo-a"}})
	require.NoError(t, err)

	wt := filepath.Join(parent.Path, "repo-a")
	require.NoError(t, os.WriteFile(filepath.Join(wt, "parent-only.txt"), []byte("x"), 0o644))
	runGit(t, wt, "add", ".")
	runGit(t, wt, "commit", "-m", "parent work")

	plain, err := svc.New(ctx, NewOptions{ShortName: "plain", Repos: []string{"repo-a"}, Parent: "invoice-pdf"})
	require.NoError(t, err)
	assert.NoFileExists(t, filepath.Join(plain.Path, "repo-a", "parent-only.txt"))

	stacked, err := svc.New(ctx, NewOptions{
		ShortName:             "stacked",
		Repos:                 []string{"repo-a"},
		Parent:                "invoice-pdf",
		InheritParentBranches: true,
	})
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(stacked.Path, "repo-a", "parent-only.txt"))
}

// Fanning a repo out over a tree: the target and every descendant get the
// repo on their own feature branch, and workspaces that already carry it are
// skipped rather than failing the whole run.
func TestAddRepos_recursiveFansOutToDescendants(t *testing.T) {
	root := setupRoot(t, "repo-a", "repo-b")
	svc := newSvc(t, root)
	ctx := t.Context()

	_, err := svc.New(ctx, NewOptions{ShortName: "q3-billing", Kind: KindProject, Repos: []string{"repo-a"}})
	require.NoError(t, err)
	// One child that already has repo-b, one that doesn't, one grandchild.
	_, err = svc.New(ctx, NewOptions{ShortName: "has-both", Ticket: "abc-1", Repos: []string{"repo-a", "repo-b"}, Parent: "q3-billing"})
	require.NoError(t, err)
	_, err = svc.New(ctx, NewOptions{ShortName: "has-one", Ticket: "abc-2", Repos: []string{"repo-a"}, Parent: "q3-billing"})
	require.NoError(t, err)
	_, err = svc.New(ctx, NewOptions{ShortName: "deep", Repos: []string{"repo-a"}, Parent: "q3-billing/abc-2--has-one"})
	require.NoError(t, err)

	res, err := svc.AddRepos(ctx, AddReposOptions{
		Workspace: "q3-billing",
		Repos:     []string{"repo-b"},
		Recursive: true,
	})
	require.NoError(t, err)

	byRef := make(map[string]WorkspaceAdd, len(res.Outcomes))
	for _, o := range res.Outcomes {
		byRef[o.Ref] = o
	}
	require.Len(t, byRef, 4, "target + all three descendants have an outcome")

	// Added everywhere it was missing, each on that workspace's own branch.
	require.Len(t, byRef["q3-billing"].Added, 1)
	assert.Equal(t, "ps--q3-billing", byRef["q3-billing"].Added[0].Branch)
	require.Len(t, byRef["q3-billing/abc-2--has-one"].Added, 1)
	assert.Equal(t, "ps--has-one--abc-2", byRef["q3-billing/abc-2--has-one"].Added[0].Branch)
	require.Len(t, byRef["q3-billing/abc-2--has-one/deep"].Added, 1)
	assert.Equal(t, "ps--deep", byRef["q3-billing/abc-2--has-one/deep"].Added[0].Branch)

	// Skipped where already present, with the reason on the outcome.
	assert.Empty(t, byRef["q3-billing/abc-1--has-both"].Added)
	assert.Equal(t, []string{"repo-b: already present"}, byRef["q3-billing/abc-1--has-both"].Skipped)

	// The worktrees exist on disk.
	assert.DirExists(t, filepath.Join(svc.WorkspacesDir, "q3-billing", "repo-b"))
	assert.DirExists(t, filepath.Join(svc.WorkspacesDir, "q3-billing", "abc-2--has-one", "repo-b"))
	assert.DirExists(t, filepath.Join(svc.WorkspacesDir, "q3-billing", "abc-2--has-one", "deep", "repo-b"))
}

// A child workspace directory that happens to share the repo's name must not
// be clobbered by a fan-out: it is skipped, and the reason names the child
// workspace rather than pretending the parent carries the repo.
func TestAddRepos_recursiveDoesNotClobberSameNamedChild(t *testing.T) {
	root := setupRoot(t, "repo-a", "repo-b")
	svc := newSvc(t, root)
	ctx := t.Context()

	_, err := svc.New(ctx, NewOptions{ShortName: "q3-billing", Kind: KindProject})
	require.NoError(t, err)
	// A child workspace named exactly like the repo being added.
	_, err = svc.New(ctx, NewOptions{ShortName: "repo-b", Repos: []string{"repo-a"}, Parent: "q3-billing"})
	require.NoError(t, err)

	res, err := svc.AddRepos(ctx, AddReposOptions{
		Workspace: "q3-billing",
		Repos:     []string{"repo-b"},
		Recursive: true,
	})
	require.NoError(t, err)

	require.Len(t, res.Outcomes, 2)
	assert.Empty(t, res.Outcomes[0].Added, "project must not overwrite its child workspace dir")
	assert.Equal(t, []string{"repo-b: blocked by the child workspace of the same name"}, res.Outcomes[0].Skipped)
	// The child itself gets the repo, since its own subdirs are free.
	require.Len(t, res.Outcomes[1].Added, 1)

	// The child workspace is still a workspace, not a worktree.
	got, err := svc.Get(ctx, "q3-billing/repo-b")
	require.NoError(t, err)
	assert.Equal(t, "q3-billing/repo-b", got.Ref)
}

// Removing a task that holds sub-issues takes them with it, so it needs the
// same explicit --recursive a project does.
func TestRemove_taskWithSubTasksNeedsRecursive(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)
	ctx := t.Context()

	parent, err := svc.New(ctx, NewOptions{ShortName: "invoice-pdf", Repos: []string{"repo-a"}})
	require.NoError(t, err)
	_, err = svc.New(ctx, NewOptions{ShortName: "fonts", Repos: []string{"repo-a"}, Parent: "invoice-pdf"})
	require.NoError(t, err)

	_, err = svc.Remove(ctx, RemoveOptions{Name: "invoice-pdf"})
	var notEmpty *ErrNotEmpty
	require.ErrorAs(t, err, &notEmpty)
	assert.Equal(t, []string{"invoice-pdf/fonts"}, notEmpty.Children)
	assert.DirExists(t, parent.Path)

	_, err = svc.Remove(ctx, RemoveOptions{Name: "invoice-pdf", Recursive: true})
	require.NoError(t, err)
	assert.NoDirExists(t, parent.Path)
}

func TestNew_unknownParent(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)

	_, err := svc.New(t.Context(), NewOptions{ShortName: "x", Repos: []string{"repo-a"}, Parent: "nope"})
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestRemove_projectWithChildrenNeedsRecursive(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)
	ctx := t.Context()

	proj, err := svc.New(ctx, NewOptions{ShortName: "q3-billing", Kind: KindProject, Repos: []string{"repo-a"}})
	require.NoError(t, err)
	child, err := svc.New(ctx, NewOptions{ShortName: "invoice", Repos: []string{"repo-a"}, Parent: "q3-billing"})
	require.NoError(t, err)

	_, err = svc.Remove(ctx, RemoveOptions{Name: "q3-billing"})
	var notEmpty *ErrNotEmpty
	require.ErrorAs(t, err, &notEmpty)
	assert.Equal(t, []string{"q3-billing/invoice"}, notEmpty.Children)
	assert.DirExists(t, proj.Path, "a refused removal must leave the project in place")
	assert.DirExists(t, child.Path)

	// --force is about losing changes, not workspaces: it must not stand in
	// for --recursive.
	_, err = svc.Remove(ctx, RemoveOptions{Name: "q3-billing", Force: true})
	require.ErrorAs(t, err, &notEmpty)
	assert.DirExists(t, proj.Path)

	_, err = svc.Remove(ctx, RemoveOptions{Name: "q3-billing", Recursive: true})
	require.NoError(t, err)
	assert.NoDirExists(t, proj.Path)

	// Both the project's and the child's branches are gone from the
	// canonical clone, so no worktree was orphaned.
	out := gitOutput(t, filepath.Join(root, "repo-a"), "branch", "--list")
	assert.NotContains(t, out, "ps--q3-billing")
	assert.NotContains(t, out, "ps--invoice")
}

func TestRemove_recursiveHonoursChildPreconditions(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)
	ctx := t.Context()

	_, err := svc.New(ctx, NewOptions{ShortName: "q3-billing", Kind: KindProject})
	require.NoError(t, err)
	child, err := svc.New(ctx, NewOptions{ShortName: "invoice", Repos: []string{"repo-a"}, Parent: "q3-billing"})
	require.NoError(t, err)

	// Dirty the child's worktree. Removing the project must refuse, because
	// the uncommitted work lives below it.
	require.NoError(t, os.WriteFile(filepath.Join(child.Path, "repo-a", "README"), []byte("changed"), 0o644))

	_, err = svc.Remove(ctx, RemoveOptions{Name: "q3-billing", Recursive: true})
	var pre *ErrPrecondition
	require.ErrorAs(t, err, &pre)
	assert.Contains(t, pre.Error(), "uncommitted changes")

	_, err = svc.Remove(ctx, RemoveOptions{Name: "q3-billing", Recursive: true, Force: true})
	require.NoError(t, err)
}

func TestRemove_childLeavesProjectIntact(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)
	ctx := t.Context()

	proj, err := svc.New(ctx, NewOptions{ShortName: "q3-billing", Kind: KindProject, Repos: []string{"repo-a"}})
	require.NoError(t, err)
	_, err = svc.New(ctx, NewOptions{ShortName: "invoice", Repos: []string{"repo-a"}, Parent: "q3-billing"})
	require.NoError(t, err)

	_, err = svc.Remove(ctx, RemoveOptions{Name: "q3-billing/invoice"})
	require.NoError(t, err)

	assert.NoDirExists(t, filepath.Join(proj.Path, "invoice"))
	assert.DirExists(t, proj.Path)
	assert.DirExists(t, filepath.Join(proj.Path, "repo-a"), "the project's own worktree survives")
}

func TestAttachTicket_nestedWorkspaceStaysInProject(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)
	ctx := t.Context()

	proj, err := svc.New(ctx, NewOptions{ShortName: "q3-billing", Kind: KindProject})
	require.NoError(t, err)
	_, err = svc.New(ctx, NewOptions{ShortName: "invoice", Repos: []string{"repo-a"}, Parent: "q3-billing"})
	require.NoError(t, err)

	res, err := svc.AttachTicket(ctx, AttachOptions{Name: "q3-billing/invoice", Ticket: "abc-42"})
	require.NoError(t, err)

	assert.Equal(t, "abc-42--invoice", res.Workspace.Name)
	assert.Equal(t, "q3-billing/abc-42--invoice", res.Workspace.Ref)
	assert.Equal(t, filepath.Join(proj.Path, "abc-42--invoice"), res.Workspace.Path)
	assert.DirExists(t, filepath.Join(proj.Path, "abc-42--invoice"))
	assert.NoDirExists(t, filepath.Join(proj.Path, "invoice"))

	out := gitOutput(t, filepath.Join(root, "repo-a"), "branch", "--list")
	assert.Contains(t, out, "ps--invoice--abc-42")
}

func TestAttachTicket_rejectsProject(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)
	ctx := t.Context()

	_, err := svc.New(ctx, NewOptions{ShortName: "q3-billing", Kind: KindProject})
	require.NoError(t, err)

	_, err = svc.AttachTicket(ctx, AttachOptions{Name: "q3-billing", Ticket: "abc-1"})
	assert.ErrorIs(t, err, ErrInvalidInput)
}

// After attach renames the workspace dir, the canonical repos must know the
// worktrees' new paths — including those of nested workspaces, whose repos
// the renamed workspace may not carry itself. Without the repair carrying
// explicit paths, the canonical repo keeps pointing at the dead old path and
// the follow-up rm fails outright.
func TestAttachTicket_repairsCanonicalRegistrationsIncludingChildren(t *testing.T) {
	root := setupRoot(t, "repo-a", "repo-b")
	svc := newSvc(t, root)
	ctx := t.Context()

	_, err := svc.New(ctx, NewOptions{ShortName: "invoice", Repos: []string{"repo-a"}})
	require.NoError(t, err)
	// The child uses a repo the parent does not carry.
	_, err = svc.New(ctx, NewOptions{ShortName: "fonts", Repos: []string{"repo-b"}, Parent: "invoice"})
	require.NoError(t, err)

	res, err := svc.AttachTicket(ctx, AttachOptions{Name: "invoice", Ticket: "abc-9"})
	require.NoError(t, err)
	assert.Empty(t, res.Warnings)

	// Both canonical repos list the post-rename worktree paths, with no
	// prunable leftovers pointing at the old ones.
	newPath := filepath.Join(svc.WorkspacesDir, "abc-9--invoice")
	listA := gitOutput(t, filepath.Join(root, "repo-a"), "worktree", "list", "--porcelain")
	assert.Contains(t, listA, filepath.Join(newPath, "repo-a"))
	assert.NotContains(t, listA, "prunable")
	listB := gitOutput(t, filepath.Join(root, "repo-b"), "worktree", "list", "--porcelain")
	assert.Contains(t, listB, filepath.Join(newPath, "fonts", "repo-b"))
	assert.NotContains(t, listB, "prunable")

	// The proof that matters day-to-day: removing the attached workspace
	// works.
	_, err = svc.Remove(ctx, RemoveOptions{Name: "abc-9--invoice", Recursive: true})
	require.NoError(t, err)
	assert.NotContains(t, gitOutput(t, filepath.Join(root, "repo-a"), "worktree", "list"), "abc-9--invoice")
}

// A single-repo workspace's dir is itself a worktree; a child created inside
// it would live inside the repo and be invisible to every tree walk.
func TestNew_rejectsWorktreeAsParent(t *testing.T) {
	root := setupRoot(t, "repo-a", "repo-b")
	svc := newSvc(t, root)
	ctx := t.Context()

	// A legacy single-repo layout: the workspace dir is the worktree.
	legacy := filepath.Join(svc.WorkspacesDir, "legacy")
	require.NoError(t, os.MkdirAll(svc.WorkspacesDir, 0o755))
	runGit(t, root+"/repo-a", "worktree", "add", "-b", "ps--legacy", legacy, "HEAD")

	_, err := svc.New(ctx, NewOptions{ShortName: "sub", Repos: []string{"repo-b"}, Parent: "legacy"})
	require.ErrorIs(t, err, ErrInvalidInput)
	assert.Contains(t, err.Error(), "single-repo workspace")

	// A project's repo worktree is not addressable as a parent at all: the
	// marker-less directory does not resolve to a workspace.
	_, err = svc.New(ctx, NewOptions{ShortName: "p", Kind: KindProject, Repos: []string{"repo-a"}})
	require.NoError(t, err)
	_, err = svc.New(ctx, NewOptions{ShortName: "sub", Repos: []string{"repo-b"}, Parent: "p/repo-a"})
	assert.ErrorIs(t, err, ErrNotFound)
}

// The read side stops descending at maxDepth; creating beyond it would build
// workspaces rm --recursive cannot see (and so cannot safety-check).
func TestNew_refusesNestingBeyondDepthCap(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)
	ctx := t.Context()

	_, err := svc.New(ctx, NewOptions{ShortName: "p", Kind: KindProject})
	require.NoError(t, err)
	parent := "p"
	for i := 1; parent != "" && i < maxDepth; i++ {
		name := fmt.Sprintf("t%d", i)
		ws, err := svc.New(ctx, NewOptions{ShortName: name, Repos: []string{"repo-a"}, Parent: parent})
		if strings.Count(parent, "/")+2 > maxDepth {
			require.Error(t, err)
			break
		}
		require.NoError(t, err, "depth %d", i)
		parent = ws.Ref
	}

	// The next level down is refused, and nothing was created on disk.
	_, err = svc.New(ctx, NewOptions{ShortName: "toodeep", Repos: []string{"repo-a"}, Parent: parent})
	require.ErrorIs(t, err, ErrInvalidInput)
	assert.Contains(t, err.Error(), "nesting deeper")

	// Everything that was created is visible to the walk.
	items, err := svc.List(ctx, ListOptions{})
	require.NoError(t, err)
	all := Flatten(items)
	for _, ws := range all {
		assert.NoDirExists(t, filepath.Join(ws.Path, "toodeep"))
	}
}

// Same short name under two parents collides on the branch name; the
// pre-check turns the mid-create git fatal into an up-front conflict.
func TestNew_branchCollisionIsAConflictNotAGitError(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)
	ctx := t.Context()

	for _, p := range []string{"p", "q"} {
		_, err := svc.New(ctx, NewOptions{ShortName: p, Kind: KindProject})
		require.NoError(t, err)
	}
	_, err := svc.New(ctx, NewOptions{ShortName: "dup", Repos: []string{"repo-a"}, Parent: "p"})
	require.NoError(t, err)

	_, err = svc.New(ctx, NewOptions{ShortName: "dup", Repos: []string{"repo-a"}, Parent: "q"})
	require.ErrorIs(t, err, ErrAlreadyExists)
	assert.Contains(t, err.Error(), "branch ps--dup already exists in repo-a")
	assert.NoDirExists(t, filepath.Join(svc.WorkspacesDir, "q", "dup"))
}

// Get must never hydrate a directory that merely exists under workspaces_dir:
// a path into a repo worktree is not a workspace, and treating it as one
// hands os.RemoveAll an arbitrary directory.
func TestGet_refusesDirectoriesInsideWorktrees(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)
	ctx := t.Context()

	ws, err := svc.New(ctx, NewOptions{ShortName: "foo", Repos: []string{"repo-a"}})
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(ws.Path, "repo-a", "src"), 0o755))

	_, err = svc.Get(ctx, "foo/repo-a")
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = svc.Get(ctx, "foo/repo-a/src")
	assert.ErrorIs(t, err, ErrNotFound)
}

// A top-level workspace and a nested one sharing a bare name: the bare name
// is ambiguous (silently preferring either would let rm delete the wrong
// one), the anchored ./ form pins the top-level workspace.
func TestGet_topLevelDoesNotShadowNested(t *testing.T) {
	root := setupRoot(t, "repo-a", "repo-b")
	svc := newSvc(t, root)
	ctx := t.Context()

	_, err := svc.New(ctx, NewOptions{ShortName: "dupe", Repos: []string{"repo-a"}})
	require.NoError(t, err)
	_, err = svc.New(ctx, NewOptions{ShortName: "p", Kind: KindProject})
	require.NoError(t, err)
	// A different repo set: the same name with an overlapping repo would be
	// (correctly) refused up front as a branch collision.
	_, err = svc.New(ctx, NewOptions{ShortName: "dupe", Repos: []string{"repo-b"}, Parent: "p"})
	require.NoError(t, err)

	_, err = svc.Get(ctx, "dupe")
	var amb *ErrAmbiguous
	require.ErrorAs(t, err, &amb)

	got, err := svc.Get(ctx, "./dupe")
	require.NoError(t, err)
	assert.Equal(t, "dupe", got.Ref)

	got, err = svc.Get(ctx, "p/dupe")
	require.NoError(t, err)
	assert.Equal(t, "p/dupe", got.Ref)
}

// The default listing must answer from the filesystem alone: names, refs,
// and branches are all present, while state that needs git subprocesses
// (dirty and friends) is deliberately absent even when true on disk.
func TestListLight_branchesWithoutGitState(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)
	ctx := t.Context()

	ws, err := svc.New(ctx, NewOptions{ShortName: "feat", Ticket: "abc-1", Repos: []string{"repo-a"}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(ws.Path, "repo-a", "dirty.txt"), []byte("x"), 0o644))

	light, err := svc.List(ctx, ListOptions{Detail: DetailLight})
	require.NoError(t, err)
	require.Len(t, light, 1)
	require.Len(t, light[0].Repos, 1)
	assert.Equal(t, "repo-a", light[0].Repos[0].Name)
	assert.Equal(t, "ps--feat--abc-1", light[0].Repos[0].Branch)
	assert.False(t, light[0].Repos[0].Dirty, "light mode does not run git, so state is unavailable")

	full, err := svc.List(ctx, ListOptions{})
	require.NoError(t, err)
	assert.True(t, full[0].Repos[0].Dirty, "full mode sees the same tree as dirty")
}
