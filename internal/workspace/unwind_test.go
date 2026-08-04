package workspace

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/data-pata/arat/internal/git"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failingGit wraps the real git wrapper and fails WorktreeAdd for one repo,
// so New's failure paths run against real git state. This is the shape of
// failure `arat new` actually sees: one repo of a set erroring while the
// others already succeeded.
type failingGit struct {
	Git
	failAddIn string // canonical repo dir substring whose add fails
}

func (f *failingGit) WorktreeAdd(ctx context.Context, repoDir, branch, target, base string) error {
	if strings.Contains(repoDir, f.failAddIn) {
		return errors.New("injected worktree add failure")
	}
	return f.Git.WorktreeAdd(ctx, repoDir, branch, target, base)
}

// newSvcWithGit is newSvc with an injectable Git implementation.
func newSvcWithGit(t *testing.T, root string, g Git) *Service {
	t.Helper()
	svc := newSvc(t, root)
	svc.Git = g
	return svc
}

// removeFailGit wraps the real git wrapper and fails WorktreeRemove or
// BranchDelete for paths containing the given substrings.
type removeFailGit struct {
	Git
	failRemoveIn string
	failDeleteIn string
}

func (f *removeFailGit) WorktreeRemove(ctx context.Context, repoDir, target string, force bool) error {
	if f.failRemoveIn != "" && strings.Contains(target, f.failRemoveIn) {
		return errors.New("injected worktree remove failure")
	}
	return f.Git.WorktreeRemove(ctx, repoDir, target, force)
}

func (f *removeFailGit) BranchDelete(ctx context.Context, repoDir, branch string, force bool) error {
	if f.failDeleteIn != "" && strings.Contains(repoDir, f.failDeleteIn) {
		return errors.New("injected branch delete failure")
	}
	return f.Git.BranchDelete(ctx, repoDir, branch, force)
}

func TestRemove_partialFailureReportsProgress(t *testing.T) {
	root := setupRoot(t, "repo-a", "repo-b")
	svc := newSvc(t, root)
	_, err := svc.New(t.Context(), NewOptions{ShortName: "doomed", Repos: []string{"repo-a", "repo-b"}})
	require.NoError(t, err)

	svc.Git = &removeFailGit{Git: git.New(), failRemoveIn: "repo-b"}
	res, err := svc.Remove(t.Context(), RemoveOptions{Name: "doomed"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "removed 1 of 2 worktrees")
	require.NotNil(t, res, "the partial result must ride along with the error")
	assert.Equal(t, []string{filepath.Join(root, "feat", "doomed", "repo-a")}, res.RemovedWorktrees)
	assert.Empty(t, res.Removed, "nothing fully removed yet")

	// Re-running with a working git continues from where it stopped.
	svc.Git = git.New()
	res, err = svc.Remove(t.Context(), RemoveOptions{Name: "doomed"})
	require.NoError(t, err)
	assert.Equal(t, []string{"doomed"}, res.Removed)
}

func TestRemove_branchDeleteFailureIsAWarning(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)
	_, err := svc.New(t.Context(), NewOptions{ShortName: "warned", Repos: []string{"repo-a"}})
	require.NoError(t, err)

	svc.Git = &removeFailGit{Git: git.New(), failDeleteIn: "repo-a"}
	res, err := svc.Remove(t.Context(), RemoveOptions{Name: "warned"})
	require.NoError(t, err, "a failed branch delete must not abort the removal")
	assert.Equal(t, []string{"warned"}, res.Removed)
	require.Len(t, res.Warnings, 1)
	assert.Contains(t, res.Warnings[0], "ps--warned")
	assert.Contains(t, res.Warnings[0], "not deleted")
	assert.NoDirExists(t, filepath.Join(root, "feat", "warned"))
}

func TestNew_failureUnwindsGitState(t *testing.T) {
	root := setupRoot(t, "repo-a", "repo-b")
	svc := newSvcWithGit(t, root, &failingGit{Git: git.New(), failAddIn: "repo-b"})

	_, err := svc.New(t.Context(), NewOptions{
		ShortName: "doomed",
		Repos:     []string{"repo-a", "repo-b"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "injected worktree add failure")
	assert.NotContains(t, err.Error(), "cleanup could not undo everything")

	// The workspace directory is gone and repo-a — whose add succeeded
	// before repo-b failed — carries neither the branch nor a worktree
	// registration.
	assert.NoDirExists(t, filepath.Join(root, "feat", "doomed"))
	real := git.New()
	assert.False(t, real.BranchExists(t.Context(), filepath.Join(root, "repo-a"), "ps--doomed"),
		"branch created by the successful job must be deleted on unwind")

	// The decisive assertion: retrying the identical command against a
	// non-failing Git succeeds. Before the unwind existed this tripped the
	// branch-collision pre-flight with advice that could not work.
	svc.Git = real
	ws, err := svc.New(t.Context(), NewOptions{
		ShortName: "doomed",
		Repos:     []string{"repo-a", "repo-b"},
	})
	require.NoError(t, err, "retry after a failed New must start from a clean slate")
	assert.Equal(t, "doomed", ws.Name)
}

func TestNew_failureAfterWorktreesUnwinds(t *testing.T) {
	// Force a failure in the phase after every worktree add succeeded: a
	// repo named "blocked.code-workspace" materialises as a directory
	// exactly where writeCodeWorkspace wants to write its file, so the
	// write fails once the git work is already done.
	root := setupRoot(t, "repo-a", "blocked.code-workspace")
	svc := newSvc(t, root)
	svc.GenerateCodeWorkspace = true

	_, err := svc.New(t.Context(), NewOptions{
		ShortName: "blocked",
		Repos:     []string{"repo-a", "blocked.code-workspace"},
	})
	require.Error(t, err)

	assert.NoDirExists(t, filepath.Join(root, "feat", "blocked"))
	real := git.New()
	for _, repo := range []string{"repo-a", "blocked.code-workspace"} {
		assert.False(t, real.BranchExists(t.Context(), filepath.Join(root, repo), "ps--blocked"),
			"branch in %s must not survive a post-worktree failure", repo)
	}

	// Retrying with a workable configuration must find clean repos.
	svc.GenerateCodeWorkspace = false
	_, err = svc.New(t.Context(), NewOptions{
		ShortName: "blocked",
		Repos:     []string{"repo-a", "blocked.code-workspace"},
	})
	require.NoError(t, err, "the canonical repos must be reusable after the unwind")
}
