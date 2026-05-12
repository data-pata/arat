package workspace

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/data-pata/arat/internal/git"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupRoot creates a temp "root" with N canonical repos (each with a single
// commit on `main`). Returns the root path. Tests use Base="HEAD" so no origin
// remote is needed.
func setupRoot(t *testing.T, repos ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range repos {
		repoDir := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(repoDir, 0o755))
		runGit(t, repoDir, "init", "-b", "main")
		runGit(t, repoDir, "config", "user.email", "t@t")
		runGit(t, repoDir, "config", "user.name", "t")
		require.NoError(t, os.WriteFile(filepath.Join(repoDir, "README"), []byte("hi"), 0o644))
		runGit(t, repoDir, "add", ".")
		runGit(t, repoDir, "commit", "-m", "init")
	}
	return root
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	out, err := c.CombinedOutput()
	require.NoErrorf(t, err, "git %v: %s", args, out)
}

func newSvc(t *testing.T, root string) *Service {
	t.Helper()
	svc, err := NewService(ServiceOptions{
		Root:          root,
		WorkspacesDir: filepath.Join(root, "feat"),
		BranchPrefix:  "ps",
		TicketRE:      regexp.MustCompile(`^[a-z]+-[0-9]+$`),
		TicketURL:     "https://linear.app/x/issue/{TICKET_UPPER}",
		Base:          "HEAD",
		Now:           func() time.Time { return time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC) },
		Git:           git.New(),
	})
	require.NoError(t, err)
	return svc
}

func TestNew_withTicket(t *testing.T) {
	root := setupRoot(t, "repo-a", "repo-b")
	svc := newSvc(t, root)

	ws, err := svc.New(t.Context(), NewOptions{
		ShortName: "fix-thing",
		Ticket:    "abc-99",
		Repos:     []string{"repo-a", "repo-b"},
	})
	require.NoError(t, err)
	assert.Equal(t, "abc-99--fix-thing", ws.Name)
	assert.Equal(t, "abc-99", ws.Ticket)
	assert.Equal(t, "https://linear.app/x/issue/ABC-99", ws.TicketURL)
	require.Len(t, ws.Repos, 2)
	for _, r := range ws.Repos {
		assert.Equal(t, "ps--fix-thing--abc-99", r.Branch)
		assert.True(t, dirExists(r.Path))
	}

	assertFileContains(t, filepath.Join(ws.Path, "CLAUDE.md"), "# fix-thing (ABC-99)")
	assertFileContains(t, filepath.Join(ws.Path, "CLAUDE.md"), "[ABC-99](https://linear.app/x/issue/ABC-99)")
	assertFileContains(t, filepath.Join(ws.Path, "claude_workspace", ".gitignore"), "*\n!.gitignore")
}

func TestNew_noTicket(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)

	ws, err := svc.New(t.Context(), NewOptions{
		ShortName: "experiment",
		Repos:     []string{"repo-a"},
	})
	require.NoError(t, err)
	assert.Equal(t, "experiment", ws.Name)
	assert.Empty(t, ws.Ticket)
	assert.Equal(t, "ps--experiment", ws.Repos[0].Branch)
	assertFileContains(t, filepath.Join(ws.Path, "CLAUDE.md"), "# experiment\n")
	assertFileContains(t, filepath.Join(ws.Path, "CLAUDE.md"), "no ticket attached yet")
}

func TestNew_progressEmitsPerRepo(t *testing.T) {
	root := setupRoot(t, "repo-a", "repo-b")
	svc := newSvc(t, root)

	var buf bytes.Buffer
	_, err := svc.New(t.Context(), NewOptions{
		ShortName: "fix-thing",
		Repos:     []string{"repo-a", "repo-b"},
		Progress:  &buf,
	})
	require.NoError(t, err)

	// Order is non-deterministic (per-repo work runs in parallel), so assert
	// each expected line appears exactly once rather than a fixed sequence.
	got := buf.String()
	for _, want := range []string{
		"fetching repo-a…\n",
		"creating worktree repo-a (base HEAD)…\n",
		"fetching repo-b…\n",
		"creating worktree repo-b (base HEAD)…\n",
	} {
		assert.Equal(t, 1, strings.Count(got, want), "expected one occurrence of %q in:\n%s", want, got)
	}
}

func TestNew_progressNilWriterIsSafe(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)
	_, err := svc.New(t.Context(), NewOptions{
		ShortName: "x",
		Repos:     []string{"repo-a"},
		// Progress: nil
	})
	require.NoError(t, err)
}

func TestNew_alreadyExists(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)

	_, err := svc.New(t.Context(), NewOptions{ShortName: "x", Repos: []string{"repo-a"}})
	require.NoError(t, err)
	_, err = svc.New(t.Context(), NewOptions{ShortName: "x", Repos: []string{"repo-a"}})
	require.ErrorIs(t, err, ErrAlreadyExists)
}

func TestNew_unknownRepo(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)
	_, err := svc.New(t.Context(), NewOptions{ShortName: "x", Repos: []string{"missing"}})
	require.ErrorIs(t, err, ErrNotFound)
	// partial state must be cleaned up
	assert.False(t, dirExists(filepath.Join(svc.WorkspacesDir, "x")))
}

func TestNew_invalidShortName(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)
	for _, bad := range []string{"", "Foo", "foo--bar", "-foo", "foo-", "foo bar"} {
		t.Run(bad, func(t *testing.T) {
			_, err := svc.New(t.Context(), NewOptions{ShortName: bad, Repos: []string{"repo-a"}})
			require.Error(t, err)
		})
	}
}

func TestNew_invalidTicket(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)
	for _, bad := range []string{"BAD!", "not-a-ticket", "abc-", "-99", "ABC-99"} {
		t.Run(bad, func(t *testing.T) {
			_, err := svc.New(t.Context(), NewOptions{ShortName: "x" + bad[:1], Ticket: bad, Repos: []string{"repo-a"}})
			require.ErrorIs(t, err, ErrInvalidInput)
		})
	}
}

func TestNew_invalidShortName_isInvalidInput(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)
	_, err := svc.New(t.Context(), NewOptions{ShortName: "Bad", Repos: []string{"repo-a"}})
	require.ErrorIs(t, err, ErrInvalidInput)
}

func TestAttachTicket_badTicket_isInvalidInput(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)
	_, err := svc.New(t.Context(), NewOptions{ShortName: "x", Repos: []string{"repo-a"}})
	require.NoError(t, err)
	_, err = svc.AttachTicket(t.Context(), AttachOptions{Name: "x", Ticket: "BAD!"})
	require.ErrorIs(t, err, ErrInvalidInput)
}

func TestAttachTicket_emptyTicket_isInvalidInput(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)
	_, err := svc.New(t.Context(), NewOptions{ShortName: "x", Repos: []string{"repo-a"}})
	require.NoError(t, err)
	_, err = svc.AttachTicket(t.Context(), AttachOptions{Name: "x", Ticket: ""})
	require.ErrorIs(t, err, ErrInvalidInput)
}

func TestRemove_clean(t *testing.T) {
	root := setupRoot(t, "repo-a", "repo-b")
	svc := newSvc(t, root)

	_, err := svc.New(t.Context(), NewOptions{ShortName: "x", Repos: []string{"repo-a", "repo-b"}})
	require.NoError(t, err)

	_, err = svc.Remove(t.Context(), RemoveOptions{Name: "x"})
	require.NoError(t, err)
	assert.False(t, dirExists(filepath.Join(svc.WorkspacesDir, "x")))

	// Branches must be deleted from canonical repos.
	for _, repo := range []string{"repo-a", "repo-b"} {
		out, err := exec.Command("git", "-C", filepath.Join(root, repo), "branch", "--list", "ps--x").CombinedOutput()
		require.NoError(t, err)
		assert.Empty(t, string(out), "branch should be deleted in %s", repo)
	}
}

func TestRemove_keepsBranches(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)

	_, err := svc.New(t.Context(), NewOptions{ShortName: "x", Repos: []string{"repo-a"}})
	require.NoError(t, err)
	_, err = svc.Remove(t.Context(), RemoveOptions{Name: "x", KeepBranches: true})
	require.NoError(t, err)

	out, err := exec.Command("git", "-C", filepath.Join(root, "repo-a"), "branch", "--list", "ps--x").CombinedOutput()
	require.NoError(t, err)
	assert.Contains(t, string(out), "ps--x")
}

func TestRemove_stashAllowedAndPreserved(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)

	ws, err := svc.New(t.Context(), NewOptions{ShortName: "x", Repos: []string{"repo-a"}})
	require.NoError(t, err)

	// Create an untracked file, add+stash so the worktree is clean but has a stash.
	require.NoError(t, os.WriteFile(filepath.Join(ws.Repos[0].Path, "wip.txt"), []byte("x"), 0o644))
	runGit(t, ws.Repos[0].Path, "add", ".")
	runGit(t, ws.Repos[0].Path, "stash", "push", "-m", "wip")

	res, err := svc.Remove(t.Context(), RemoveOptions{Name: "x"})
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Len(t, res.StashedRepos, 1)
	assert.Equal(t, ws.Repos[0].Path, res.StashedRepos[0].Path)
	assert.Equal(t, filepath.Join(root, "repo-a"), res.StashedRepos[0].CanonicalRepo)
	assert.Equal(t, 1, res.StashedRepos[0].Stashes)

	// Workspace dir is gone, but the stash ref still lives in the canonical
	// clone — this is the whole point of the change.
	assert.False(t, dirExists(ws.Path))
	out, gerr := exec.Command("git", "-C", filepath.Join(root, "repo-a"), "stash", "list").CombinedOutput()
	require.NoError(t, gerr, "stash list in canonical: %s", out)
	assert.Contains(t, string(out), "wip", "stash entry must survive worktree removal")
}

func TestRemove_noStashesMeansEmptyResult(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)

	_, err := svc.New(t.Context(), NewOptions{ShortName: "x", Repos: []string{"repo-a"}})
	require.NoError(t, err)

	res, err := svc.Remove(t.Context(), RemoveOptions{Name: "x"})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Empty(t, res.StashedRepos, "no stashes -> nothing to surface")
}

func TestRemove_unpushedRefuses(t *testing.T) {
	root := t.TempDir()
	// build a canonical repo with an origin so unpushed-detection works
	canonical := filepath.Join(root, "repo-a")
	bare := filepath.Join(root, "repo-a.bare")
	require.NoError(t, os.MkdirAll(canonical, 0o755))
	runGit(t, canonical, "init", "-b", "main")
	runGit(t, canonical, "config", "user.email", "t@t")
	runGit(t, canonical, "config", "user.name", "t")
	require.NoError(t, os.WriteFile(filepath.Join(canonical, "README"), []byte("x"), 0o644))
	runGit(t, canonical, "add", ".")
	runGit(t, canonical, "commit", "-m", "init")
	runGit(t, root, "init", "--bare", "repo-a.bare")
	runGit(t, canonical, "remote", "add", "origin", bare)
	runGit(t, canonical, "push", "origin", "main")

	svc := newSvc(t, root)
	svc.Base = "origin/HEAD" // use real flow now that origin exists

	ws, err := svc.New(t.Context(), NewOptions{ShortName: "x", Repos: []string{"repo-a"}})
	require.NoError(t, err)

	// commit something on the worktree's branch and DON'T push
	require.NoError(t, os.WriteFile(filepath.Join(ws.Repos[0].Path, "new.txt"), []byte("x"), 0o644))
	runGit(t, ws.Repos[0].Path, "add", ".")
	runGit(t, ws.Repos[0].Path, "commit", "-m", "wip")
	// Set upstream so unpushed-check has a baseline.
	runGit(t, ws.Repos[0].Path, "branch", "--set-upstream-to=origin/main")

	_, err = svc.Remove(t.Context(), RemoveOptions{Name: "x"})
	var pre *ErrPrecondition
	require.ErrorAs(t, err, &pre)
	assert.Contains(t, err.Error(), "unpushed commits")
}

func TestGet_happy(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)

	_, err := svc.New(t.Context(), NewOptions{ShortName: "x", Ticket: "abc-1", Repos: []string{"repo-a"}})
	require.NoError(t, err)

	ws, err := svc.Get(t.Context(), "abc-1--x")
	require.NoError(t, err)
	assert.Equal(t, "abc-1--x", ws.Name)
	assert.Equal(t, "abc-1", ws.Ticket)
	require.Len(t, ws.Repos, 1)
	assert.Equal(t, "repo-a", ws.Repos[0].Name)
}

func TestGet_notFound(t *testing.T) {
	root := setupRoot(t)
	svc := newSvc(t, root)
	require.NoError(t, os.MkdirAll(svc.WorkspacesDir, 0o755))
	_, err := svc.Get(t.Context(), "nope")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestGet_notDirectory(t *testing.T) {
	root := setupRoot(t)
	svc := newSvc(t, root)
	require.NoError(t, os.MkdirAll(svc.WorkspacesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(svc.WorkspacesDir, "stray"), []byte("x"), 0o644))
	_, err := svc.Get(t.Context(), "stray")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestNew_nowFallback(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)
	svc.Now = nil // fallback to time.Now()
	ws, err := svc.New(t.Context(), NewOptions{ShortName: "x", Repos: []string{"repo-a"}})
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now(), ws.Created, 5*time.Second)
}

func TestRemove_dirtyRefuses(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)

	ws, err := svc.New(t.Context(), NewOptions{ShortName: "x", Repos: []string{"repo-a"}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(ws.Repos[0].Path, "wip.txt"), []byte("dirty"), 0o644))

	_, err = svc.Remove(t.Context(), RemoveOptions{Name: "x"})
	var pre *ErrPrecondition
	require.ErrorAs(t, err, &pre)
	assert.Contains(t, err.Error(), "uncommitted changes")
	// workspace must still exist after refusal
	assert.True(t, dirExists(ws.Path))

	// --force overrides
	_, err = svc.Remove(t.Context(), RemoveOptions{Name: "x", Force: true})
	require.NoError(t, err)
	assert.False(t, dirExists(ws.Path))
}

func TestRemove_notFound(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)
	_, err := svc.Remove(t.Context(), RemoveOptions{Name: "nope"})
	require.ErrorIs(t, err, ErrNotFound)
}

func TestAttachTicket_happy(t *testing.T) {
	root := setupRoot(t, "repo-a", "repo-b")
	svc := newSvc(t, root)

	// Wire a fake Claude projects dir so we can verify session migration
	// happens as part of attach, end-to-end.
	claudeProjects := filepath.Join(t.TempDir(), "projects")
	require.NoError(t, os.MkdirAll(claudeProjects, 0o755))
	svc.ClaudeProjectsDir = claudeProjects

	_, err := svc.New(t.Context(), NewOptions{ShortName: "myfeat", Repos: []string{"repo-a", "repo-b"}})
	require.NoError(t, err)

	// Simulate two Claude sessions that ran while the workspace was named
	// "myfeat": one at the workspace root, one inside the repo-a worktree.
	oldRootEnc := EncodeCwdAsProjectDir(filepath.Join(svc.WorkspacesDir, "myfeat"))
	oldRepoAEnc := EncodeCwdAsProjectDir(filepath.Join(svc.WorkspacesDir, "myfeat", "repo-a"))
	for _, dir := range []string{oldRootEnc, oldRepoAEnc} {
		require.NoError(t, os.MkdirAll(filepath.Join(claudeProjects, dir), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(claudeProjects, dir, "session.jsonl"), []byte(`{}`), 0o600))
	}
	// Append user content under H2; we want it preserved.
	mdPath := filepath.Join(svc.WorkspacesDir, "myfeat", "CLAUDE.md")
	original, err := os.ReadFile(mdPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(mdPath, []byte(string(original)+"\nUser-typed line under Notes.\n"), 0o644))

	res, err := svc.AttachTicket(t.Context(), AttachOptions{Name: "myfeat", Ticket: "abc-99"})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Empty(t, res.Warnings)
	assert.Empty(t, res.SessionWarnings)
	assert.Equal(t, "abc-99--myfeat", res.Workspace.Name)
	assert.Equal(t, "abc-99", res.Workspace.Ticket)

	// Session dirs followed the rename.
	newRootEnc := EncodeCwdAsProjectDir(filepath.Join(svc.WorkspacesDir, "abc-99--myfeat"))
	newRepoAEnc := EncodeCwdAsProjectDir(filepath.Join(svc.WorkspacesDir, "abc-99--myfeat", "repo-a"))
	for _, dir := range []string{newRootEnc, newRepoAEnc} {
		_, err := os.Stat(filepath.Join(claudeProjects, dir, "session.jsonl"))
		assert.NoError(t, err, "expected jsonl at %s", dir)
	}
	for _, dir := range []string{oldRootEnc, oldRepoAEnc} {
		_, err := os.Stat(filepath.Join(claudeProjects, dir))
		assert.True(t, os.IsNotExist(err), "expected old project dir gone: %s", dir)
	}

	// Old dir gone, new dir present
	assert.False(t, dirExists(filepath.Join(svc.WorkspacesDir, "myfeat")))
	newPath := filepath.Join(svc.WorkspacesDir, "abc-99--myfeat")
	assert.True(t, dirExists(newPath))

	// Branches renamed in each canonical repo
	for _, repo := range []string{"repo-a", "repo-b"} {
		out, err := exec.Command("git", "-C", filepath.Join(root, repo), "branch", "--list", "ps--myfeat--abc-99").CombinedOutput()
		require.NoError(t, err)
		assert.Contains(t, string(out), "ps--myfeat--abc-99")
		out, err = exec.Command("git", "-C", filepath.Join(root, repo), "branch", "--list", "ps--myfeat").CombinedOutput()
		require.NoError(t, err)
		assert.Empty(t, strings.TrimSpace(string(out)), "old branch must be gone in %s", repo)
	}

	// CLAUDE.md updated and user content preserved
	data, err := os.ReadFile(filepath.Join(newPath, "CLAUDE.md"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "# myfeat (ABC-99)")
	assert.Contains(t, string(data), "[ABC-99](https://linear.app/x/issue/ABC-99)")
	assert.Contains(t, string(data), "ps--myfeat--abc-99")
	assert.Contains(t, string(data), "User-typed line under Notes.", "user content must be preserved")
}

func TestAttachTicket_alreadyTicketed(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)
	_, err := svc.New(t.Context(), NewOptions{ShortName: "x", Ticket: "abc-1", Repos: []string{"repo-a"}})
	require.NoError(t, err)

	_, err = svc.AttachTicket(t.Context(), AttachOptions{Name: "abc-1--x", Ticket: "abc-2"})
	var pre *ErrPrecondition
	require.ErrorAs(t, err, &pre)
}

func TestAttachTicket_notFound(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)
	require.NoError(t, os.MkdirAll(svc.WorkspacesDir, 0o755))
	_, err := svc.AttachTicket(t.Context(), AttachOptions{Name: "missing", Ticket: "abc-1"})
	require.ErrorIs(t, err, ErrNotFound)
}

func TestAttachTicket_badTicket(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)
	_, err := svc.New(t.Context(), NewOptions{ShortName: "x", Repos: []string{"repo-a"}})
	require.NoError(t, err)
	_, err = svc.AttachTicket(t.Context(), AttachOptions{Name: "x", Ticket: "BAD!"})
	require.Error(t, err)
}

func TestAttachTicket_warnsOnDivergedBranch(t *testing.T) {
	root := setupRoot(t, "repo-a", "repo-b")
	svc := newSvc(t, root)
	_, err := svc.New(t.Context(), NewOptions{ShortName: "z", Repos: []string{"repo-a", "repo-b"}})
	require.NoError(t, err)

	// Switch repo-b's worktree to a different branch — attach should warn.
	wtB := filepath.Join(svc.WorkspacesDir, "z", "repo-b")
	runGit(t, wtB, "checkout", "-b", "elsewhere")

	res, err := svc.AttachTicket(t.Context(), AttachOptions{Name: "z", Ticket: "abc-7"})
	require.NoError(t, err)
	require.NotEmpty(t, res.Warnings)
	hasB := false
	for _, w := range res.Warnings {
		if w.Repo == "repo-b" {
			hasB = true
		}
	}
	assert.True(t, hasB, "warning expected for repo-b: %v", res.Warnings)
}

func TestAttachTicket_alreadyExists(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)
	_, err := svc.New(t.Context(), NewOptions{ShortName: "x", Repos: []string{"repo-a"}})
	require.NoError(t, err)
	_, err = svc.New(t.Context(), NewOptions{ShortName: "x", Ticket: "abc-1", Repos: []string{"repo-a"}})
	require.NoError(t, err)
	_, err = svc.AttachTicket(t.Context(), AttachOptions{Name: "x", Ticket: "abc-1"})
	require.ErrorIs(t, err, ErrAlreadyExists)
}

// --- Phase 7 features --------------------------------------------------

func TestNew_baseByRepo(t *testing.T) {
	root := setupRoot(t, "repo-a", "repo-b")
	svc := newSvc(t, root)

	// Create a parent ws with custom branches per repo.
	_, err := svc.New(t.Context(), NewOptions{ShortName: "parent", Repos: []string{"repo-a", "repo-b"}})
	require.NoError(t, err)
	// Make a divergent commit on repo-a's parent worktree to verify the new
	// child branches off there (the commit must exist on the child branch too).
	parentRepoA := filepath.Join(svc.WorkspacesDir, "parent", "repo-a")
	require.NoError(t, os.WriteFile(filepath.Join(parentRepoA, "x.txt"), []byte("parent"), 0o644))
	runGit(t, parentRepoA, "add", ".")
	runGit(t, parentRepoA, "commit", "-m", "parent diverged")

	// Create a child ws branching off parent's branches per repo.
	child, err := svc.New(t.Context(), NewOptions{
		ShortName: "child",
		Repos:     []string{"repo-a", "repo-b"},
		BaseByRepo: map[string]string{
			"repo-a": "ps--parent",
			"repo-b": "ps--parent",
		},
	})
	require.NoError(t, err)

	// child's repo-a should contain the parent's "x.txt"
	assert.FileExists(t, filepath.Join(child.Repos[0].Path, "x.txt"))
}

func TestNew_carryContext(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)

	ws, err := svc.New(t.Context(), NewOptions{
		ShortName: "child",
		Repos:     []string{"repo-a"},
		CarryFrom: &CarryContext{
			ParentName:      "abc-1--parent",
			ParentShortName: "parent",
			ParentTicket:    "abc-1",
			ParentTicketURL: "https://linear.app/x/issue/ABC-1",
		},
	})
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(ws.Path, "CLAUDE.md"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "Spun off from `abc-1--parent`")
	assert.Contains(t, string(data), "[ABC-1](https://linear.app/x/issue/ABC-1)")
}

func TestNew_codeWorkspaceFromConfig(t *testing.T) {
	root := setupRoot(t, "repo-a", "repo-b")
	svc := newSvc(t, root)
	svc.GenerateCodeWorkspace = true

	ws, err := svc.New(t.Context(), NewOptions{ShortName: "x", Repos: []string{"repo-a", "repo-b"}})
	require.NoError(t, err)
	cwPath := filepath.Join(ws.Path, "x.code-workspace")
	assert.FileExists(t, cwPath)
	data, err := os.ReadFile(cwPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"path": "./repo-a"`)
	assert.Contains(t, string(data), `"path": "./repo-b"`)
	assert.Contains(t, string(data), `"path": "."`) // workspace root included
}

func TestNew_codeWorkspaceFromOption(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)
	svc.GenerateCodeWorkspace = false

	ws, err := svc.New(t.Context(), NewOptions{
		ShortName:             "x",
		Repos:                 []string{"repo-a"},
		GenerateCodeWorkspace: true,
	})
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(ws.Path, "x.code-workspace"))
}

func TestNew_codeWorkspaceOff(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)

	ws, err := svc.New(t.Context(), NewOptions{ShortName: "x", Repos: []string{"repo-a"}})
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(ws.Path, "x.code-workspace"))
	assert.True(t, os.IsNotExist(err))
}

func TestResolveRepos_explicitWins(t *testing.T) {
	root := setupRoot(t, "repo-a", "repo-b", "repo-c")
	svc := newSvc(t, root)
	svc.DefaultRepos = []string{"repo-a"}
	svc.AutoReposGlob = []string{"repo-*"}

	got, err := svc.ResolveRepos([]string{"only-this"})
	require.NoError(t, err)
	assert.Equal(t, []string{"only-this"}, got)
}

func TestResolveRepos_unionDefaultAndGlob(t *testing.T) {
	root := setupRoot(t, "alpha", "core-app", "infra-k8s", "extra")
	svc := newSvc(t, root)
	svc.DefaultRepos = []string{"alpha", "missing"}
	svc.AutoReposGlob = []string{"core-*", "infra-*"}

	got, err := svc.ResolveRepos(nil)
	require.NoError(t, err)
	// default first (only existing ones), then glob matches sorted
	assert.Equal(t, []string{"alpha", "core-app", "infra-k8s"}, got)
}

func TestResolveRepos_globSkipsLinkedWorktrees(t *testing.T) {
	// core-app-bis is a linked worktree of core-app (shares git store).
	// auto_repos_glob must not pick it up — only full clones qualify.
	root := setupRoot(t, "core-app")
	runGit(t, filepath.Join(root, "core-app"), "worktree", "add", "--detach", filepath.Join(root, "core-app-bis"))

	svc := newSvc(t, root)
	svc.AutoReposGlob = []string{"core-*"}

	got, err := svc.ResolveRepos(nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"core-app"}, got)
}

func TestResolveRepos_noneResolved(t *testing.T) {
	root := setupRoot(t)
	svc := newSvc(t, root)
	_, err := svc.ResolveRepos(nil)
	require.Error(t, err)
}

func TestListRepoCandidates_preselectsDefaultPlusGlob(t *testing.T) {
	root := setupRoot(t, "alpha", "core-app", "infra-k8s", "extra", "stray")
	svc := newSvc(t, root)
	svc.DefaultRepos = []string{"alpha"}
	svc.AutoReposGlob = []string{"core-*", "infra-*"}

	got, err := svc.ListRepoCandidates()
	require.NoError(t, err)
	// Selected first (default+glob in their resolution order), then unselected
	// alphabetically.
	assert.Equal(t, []RepoCandidate{
		{Name: "alpha", Selected: true},
		{Name: "core-app", Selected: true},
		{Name: "infra-k8s", Selected: true},
		{Name: "extra", Selected: false},
		{Name: "stray", Selected: false},
	}, got)
}

func TestListRepoCandidates_skipsNonGitDirs(t *testing.T) {
	root := setupRoot(t, "repo-a")
	// Plain directory at root with no .git — must not appear.
	require.NoError(t, os.MkdirAll(filepath.Join(root, "not-a-repo"), 0o755))
	svc := newSvc(t, root)

	got, err := svc.ListRepoCandidates()
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "repo-a", got[0].Name)
	assert.False(t, got[0].Selected, "no default/glob configured: nothing preselected")
}

func TestListRepoCandidates_skipsLinkedWorktrees(t *testing.T) {
	root := setupRoot(t, "core-app")
	runGit(t, filepath.Join(root, "core-app"), "worktree", "add", "--detach", filepath.Join(root, "core-app-bis"))
	svc := newSvc(t, root)

	got, err := svc.ListRepoCandidates()
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "core-app", got[0].Name)
}

func TestListRepoCandidates_emptyRoot(t *testing.T) {
	root := setupRoot(t)
	svc := newSvc(t, root)
	got, err := svc.ListRepoCandidates()
	require.NoError(t, err)
	assert.Empty(t, got)
}

func assertFileContains(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), want)
}

// --- AddRepos --------------------------------------------------------

func TestAddRepos_happy(t *testing.T) {
	root := setupRoot(t, "repo-a", "repo-b", "repo-c")
	svc := newSvc(t, root)

	_, err := svc.New(t.Context(), NewOptions{ShortName: "feat", Ticket: "abc-1", Repos: []string{"repo-a"}})
	require.NoError(t, err)

	res, err := svc.AddRepos(t.Context(), AddReposOptions{
		Workspace: "abc-1--feat",
		Repos:     []string{"repo-b", "repo-c"},
	})
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Len(t, res.Added, 2)
	for _, r := range res.Added {
		assert.Equal(t, "ps--feat--abc-1", r.Branch)
		assert.True(t, dirExists(r.Path))
	}

	// Workspace now reports all three repos.
	require.Len(t, res.Workspace.Repos, 3)

	// Branch was created in each newly-added canonical repo.
	for _, repo := range []string{"repo-b", "repo-c"} {
		out, err := exec.Command("git", "-C", filepath.Join(root, repo), "branch", "--list", "ps--feat--abc-1").CombinedOutput()
		require.NoError(t, err)
		assert.Contains(t, string(out), "ps--feat--abc-1", "branch missing in %s", repo)
	}
}

func TestAddRepos_derivesBranchFromExistingWorktree(t *testing.T) {
	// After `ticket attach`, branches are renamed. AddRepos must use the
	// renamed branch (read off the existing worktree), not BranchName().
	root := setupRoot(t, "repo-a", "repo-b")
	svc := newSvc(t, root)

	_, err := svc.New(t.Context(), NewOptions{ShortName: "feat", Repos: []string{"repo-a"}})
	require.NoError(t, err)
	_, err = svc.AttachTicket(t.Context(), AttachOptions{Name: "feat", Ticket: "abc-7"})
	require.NoError(t, err)

	res, err := svc.AddRepos(t.Context(), AddReposOptions{
		Workspace: "abc-7--feat",
		Repos:     []string{"repo-b"},
	})
	require.NoError(t, err)
	assert.Equal(t, "ps--feat--abc-7", res.Added[0].Branch)
}

func TestAddRepos_regeneratesCodeWorkspace(t *testing.T) {
	root := setupRoot(t, "repo-a", "repo-b")
	svc := newSvc(t, root)
	svc.GenerateCodeWorkspace = true

	_, err := svc.New(t.Context(), NewOptions{ShortName: "x", Repos: []string{"repo-a"}})
	require.NoError(t, err)

	cwPath := filepath.Join(svc.WorkspacesDir, "x", "x.code-workspace")
	require.FileExists(t, cwPath)

	_, err = svc.AddRepos(t.Context(), AddReposOptions{Workspace: "x", Repos: []string{"repo-b"}})
	require.NoError(t, err)

	data, err := os.ReadFile(cwPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"path": "./repo-a"`)
	assert.Contains(t, string(data), `"path": "./repo-b"`)
}

func TestAddRepos_codeWorkspaceNotCreatedIfAbsent(t *testing.T) {
	root := setupRoot(t, "repo-a", "repo-b")
	svc := newSvc(t, root) // GenerateCodeWorkspace=false

	_, err := svc.New(t.Context(), NewOptions{ShortName: "x", Repos: []string{"repo-a"}})
	require.NoError(t, err)

	_, err = svc.AddRepos(t.Context(), AddReposOptions{Workspace: "x", Repos: []string{"repo-b"}})
	require.NoError(t, err)

	cwPath := filepath.Join(svc.WorkspacesDir, "x", "x.code-workspace")
	_, statErr := os.Stat(cwPath)
	assert.True(t, os.IsNotExist(statErr), "code-workspace must not be created here")
}

func TestAddRepos_workspaceNotFound(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)
	require.NoError(t, os.MkdirAll(svc.WorkspacesDir, 0o755))

	_, err := svc.AddRepos(t.Context(), AddReposOptions{Workspace: "missing", Repos: []string{"repo-a"}})
	require.ErrorIs(t, err, ErrNotFound)
}

func TestAddRepos_repoNotAtRoot(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)
	_, err := svc.New(t.Context(), NewOptions{ShortName: "x", Repos: []string{"repo-a"}})
	require.NoError(t, err)

	_, err = svc.AddRepos(t.Context(), AddReposOptions{Workspace: "x", Repos: []string{"missing"}})
	require.ErrorIs(t, err, ErrNotFound)
}

func TestAddRepos_alreadyInWorkspace(t *testing.T) {
	root := setupRoot(t, "repo-a", "repo-b")
	svc := newSvc(t, root)
	_, err := svc.New(t.Context(), NewOptions{ShortName: "x", Repos: []string{"repo-a", "repo-b"}})
	require.NoError(t, err)

	_, err = svc.AddRepos(t.Context(), AddReposOptions{Workspace: "x", Repos: []string{"repo-b"}})
	require.ErrorIs(t, err, ErrAlreadyExists)
}

func TestAddRepos_subdirCollides(t *testing.T) {
	root := setupRoot(t, "repo-a", "repo-b")
	svc := newSvc(t, root)
	ws, err := svc.New(t.Context(), NewOptions{ShortName: "x", Repos: []string{"repo-a"}})
	require.NoError(t, err)

	// Plant a non-worktree directory where repo-b would land.
	require.NoError(t, os.MkdirAll(filepath.Join(ws.Path, "repo-b"), 0o755))

	_, err = svc.AddRepos(t.Context(), AddReposOptions{Workspace: "x", Repos: []string{"repo-b"}})
	require.ErrorIs(t, err, ErrAlreadyExists)
}

func TestAddRepos_noReposGiven(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)
	_, err := svc.New(t.Context(), NewOptions{ShortName: "x", Repos: []string{"repo-a"}})
	require.NoError(t, err)

	_, err = svc.AddRepos(t.Context(), AddReposOptions{Workspace: "x"})
	require.Error(t, err)
}
