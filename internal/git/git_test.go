package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInspect_realRepo(t *testing.T) {
	dir := initRepo(t)

	g := New()
	ins, err := g.Inspect(t.Context(), dir)
	require.NoError(t, err)
	assert.NotEmpty(t, ins.Branch)
	assert.False(t, ins.Dirty)
	assert.Equal(t, 0, ins.Stashes)
}

func TestInspect_dirtyAndStash(t *testing.T) {
	dir := initRepo(t)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("hi"), 0o644))

	g := New()
	ins, err := g.Inspect(t.Context(), dir)
	require.NoError(t, err)
	assert.True(t, ins.Dirty)

	mustGit(t, dir, "add", ".")
	mustGit(t, dir, "stash", "push", "-m", "wip")
	ins, err = g.Inspect(t.Context(), dir)
	require.NoError(t, err)
	assert.False(t, ins.Dirty, "after stash the tree should be clean")
	assert.Equal(t, 1, ins.Stashes)
}

func TestInspect_notAWorktree(t *testing.T) {
	dir := t.TempDir()
	g := New()
	_, err := g.Inspect(t.Context(), dir)
	require.Error(t, err)
}

func TestIsWorktree(t *testing.T) {
	g := New()
	assert.False(t, g.IsWorktree(t.Context(), t.TempDir()))
	assert.True(t, g.IsWorktree(t.Context(), initRepo(t)))
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustGit(t, dir, "init", "-b", "main")
	mustGit(t, dir, "config", "user.email", "test@example.com")
	mustGit(t, dir, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README"), []byte("hi"), 0o644))
	mustGit(t, dir, "add", ".")
	mustGit(t, dir, "commit", "-m", "init")
	return dir
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git %v: %s", args, out)
}

func TestCanonicalRepo(t *testing.T) {
	dir := initRepo(t)

	g := New()
	assert.Equal(t, filepath.Base(dir), g.CanonicalRepoName(t.Context(), dir))
	assert.Equal(t, dir, g.CanonicalRepoPath(t.Context(), dir))

	// Empty dir returns "" (not a worktree).
	empty := t.TempDir()
	assert.Equal(t, "", g.CanonicalRepoName(t.Context(), empty))
	assert.Equal(t, "", g.CanonicalRepoPath(t.Context(), empty))
}

func TestWorktreeAddRemoveAndBranchDelete(t *testing.T) {
	canonical := initRepo(t)
	wtPath := filepath.Join(t.TempDir(), "wt")

	g := New()

	// add a worktree from HEAD on a fresh branch
	require.NoError(t, g.WorktreeAdd(t.Context(), canonical, "feat/x", wtPath, "HEAD"))
	assert.True(t, g.IsWorktree(t.Context(), wtPath))

	ins, err := g.Inspect(t.Context(), wtPath)
	require.NoError(t, err)
	assert.Equal(t, "feat/x", ins.Branch)

	// remove the worktree
	require.NoError(t, g.WorktreeRemove(t.Context(), canonical, wtPath, false))
	assert.False(t, g.IsWorktree(t.Context(), wtPath))

	// delete the branch
	require.NoError(t, g.BranchDelete(t.Context(), canonical, "feat/x", true))

	// re-deleting fails (branch gone)
	require.Error(t, g.BranchDelete(t.Context(), canonical, "feat/x", true))
}

func TestBranchRename_andRepair(t *testing.T) {
	canonical := initRepo(t)
	wtPath := filepath.Join(t.TempDir(), "wt")

	g := New()
	require.NoError(t, g.WorktreeAdd(t.Context(), canonical, "old", wtPath, "HEAD"))

	// rename branch from inside the worktree
	require.NoError(t, g.BranchRename(t.Context(), wtPath, "old", "new"))
	ins, err := g.Inspect(t.Context(), wtPath)
	require.NoError(t, err)
	assert.Equal(t, "new", ins.Branch)

	// rename to same name is a no-op
	require.NoError(t, g.BranchRename(t.Context(), wtPath, "new", "new"))

	// move the worktree directory (simulate `arat ticket attach` rename)
	moved := filepath.Join(filepath.Dir(wtPath), "moved")
	require.NoError(t, os.Rename(wtPath, moved))

	// repair from the canonical repo so it knows the worktree's new path
	require.NoError(t, g.WorktreeRepair(t.Context(), canonical))

	// IsWorktree on moved path should now resolve correctly
	assert.True(t, g.IsWorktree(t.Context(), moved))
}

func TestFetch_noOrigin(t *testing.T) {
	dir := initRepo(t)
	g := New()
	err := g.Fetch(t.Context(), dir)
	require.Error(t, err, "fetch should fail when no origin remote is configured")
}

func TestRunner_fakeable(t *testing.T) {
	calls := 0
	fake := func(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, error) {
		calls++
		switch args[0] {
		case "rev-parse":
			return []byte("true\n"), nil, nil
		case "branch":
			return []byte("feat/x\n"), nil, nil
		case "status":
			return []byte(""), nil, nil
		case "log":
			return []byte("abc commit\n"), nil, nil // simulate unpushed
		case "stash":
			return []byte(""), nil, nil
		}
		return nil, nil, nil
	}
	g := NewWithRunner(fake)
	ins, err := g.Inspect(t.Context(), "/anywhere")
	require.NoError(t, err)
	assert.Equal(t, "feat/x", ins.Branch)
	assert.False(t, ins.Dirty)
	assert.True(t, ins.Unpushed)
	assert.Greater(t, calls, 3)
}
