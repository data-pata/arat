package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
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

func TestNew_insideProject_nestsAndInheritsBase(t *testing.T) {
	root := setupRoot(t, "repo-a", "repo-b")
	svc := newSvc(t, root)
	ctx := t.Context()

	// A project that carries its own worktree on an integration branch.
	proj, err := svc.New(ctx, NewOptions{
		ShortName: "q3-billing",
		Kind:      KindProject,
		Repos:     []string{"repo-a"},
	})
	require.NoError(t, err)
	require.Len(t, proj.Repos, 1)
	assert.Equal(t, "ps--q3-billing", proj.Repos[0].Branch)

	// Commit on the project branch so the child has something to inherit
	// that origin/HEAD does not have.
	wt := filepath.Join(proj.Path, "repo-a")
	require.NoError(t, os.WriteFile(filepath.Join(wt, "project-only.txt"), []byte("x"), 0o644))
	runGit(t, wt, "add", ".")
	runGit(t, wt, "commit", "-m", "project scaffolding")

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

	// The child branched off the project's branch, so the project-only
	// commit is present in the child's worktree.
	assert.FileExists(t, filepath.Join(child.Path, "repo-a", "project-only.txt"))

	// The tree reflects the nesting.
	items, err := svc.List(ctx)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Len(t, items[0].Children, 1)
	assert.Equal(t, "q3-billing/abc-12--invoice-pdf", items[0].Children[0].Ref)
}

func TestNew_insideProject_repoNotInProjectUsesDefaultBase(t *testing.T) {
	root := setupRoot(t, "repo-a", "repo-b")
	svc := newSvc(t, root)
	ctx := t.Context()

	_, err := svc.New(ctx, NewOptions{ShortName: "q3-billing", Kind: KindProject, Repos: []string{"repo-a"}})
	require.NoError(t, err)

	// repo-b has no counterpart in the project, so it falls back to Base.
	child, err := svc.New(ctx, NewOptions{
		ShortName: "wider",
		Repos:     []string{"repo-a", "repo-b"},
		Parent:    "q3-billing",
	})
	require.NoError(t, err)
	assert.Len(t, child.Repos, 2)
}

func TestNew_parentMustBeAProject(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)
	ctx := t.Context()

	_, err := svc.New(ctx, NewOptions{ShortName: "leaf", Repos: []string{"repo-a"}})
	require.NoError(t, err)

	_, err = svc.New(ctx, NewOptions{ShortName: "deeper", Repos: []string{"repo-a"}, Parent: "leaf"})
	assert.ErrorIs(t, err, ErrInvalidInput)
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
