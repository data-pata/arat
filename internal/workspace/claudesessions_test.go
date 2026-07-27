package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncodeCwdAsProjectDir(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"/home/u/git/myorg/feat/foo", "-home-u-git-myorg-feat-foo"},
		{"/home/u/.claude", "-home-u--claude"},
		{"/home/u/git/myorg/feat/abc-1--xyz", "-home-u-git-myorg-feat-abc-1--xyz"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			assert.Equal(t, tc.want, EncodeCwdAsProjectDir(tc.in))
		})
	}
}

// claudeRig sets up a fake ~/.claude/projects/ with the given subdirs each
// containing one jsonl file (named after the dir for traceability). Returns
// the projects-root path so callers can wire it into Service.ClaudeProjectsDir.
func claudeRig(t *testing.T, dirs ...string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "projects")
	require.NoError(t, os.MkdirAll(root, 0o755))
	for _, d := range dirs {
		full := filepath.Join(root, d)
		require.NoError(t, os.MkdirAll(full, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(full, d+".jsonl"), []byte(`{"cwd":"x"}`), 0o600))
	}
	return root
}

func TestMoveSessionsForRename_renamesWorkspaceAndSubdirs(t *testing.T) {
	wsDir := t.TempDir()
	// Pretend the workspace dir has already been renamed to oldName→newName.
	oldPath := filepath.Join(wsDir, "abc-1--myfeat")
	require.NoError(t, os.MkdirAll(oldPath, 0o755))

	// Encoded names mirror what Claude Code would have created when the
	// chat was running in the pre-rename dir and its repo-worktree subdirs.
	oldEnc := EncodeCwdAsProjectDir(filepath.Join(wsDir, "myfeat"))
	newEnc := EncodeCwdAsProjectDir(oldPath)
	root := claudeRig(t,
		oldEnc,
		oldEnc+"-repo-a",
		oldEnc+"-repo-b",
		"-unrelated-other",
	)

	s := &Service{WorkspacesDir: wsDir, ClaudeProjectsDir: root}
	warnings := s.MoveSessionsForRename(filepath.Join(wsDir, "myfeat"), oldPath)
	assert.Empty(t, warnings)

	// Old encoded dirs gone, new ones present with file moved.
	for _, name := range []string{oldEnc, oldEnc + "-repo-a", oldEnc + "-repo-b"} {
		_, err := os.Stat(filepath.Join(root, name))
		assert.True(t, os.IsNotExist(err), "old project dir %s should be gone", name)
	}
	for _, suffix := range []string{"", "-repo-a", "-repo-b"} {
		newDir := newEnc + suffix
		entries, err := os.ReadDir(filepath.Join(root, newDir))
		require.NoError(t, err, "new project dir %s missing", newDir)
		assert.NotEmpty(t, entries, "new project dir %s should contain the jsonl", newDir)
	}
	// Unrelated dir is untouched.
	_, err := os.Stat(filepath.Join(root, "-unrelated-other"))
	assert.NoError(t, err)
}

func TestMoveSessionsForRename_skipsSiblingWorkspaceCollisions(t *testing.T) {
	// Workspaces `foo` and `foo-extra` coexist. Renaming `foo` to `abc-1--foo`
	// must not touch project dirs that belong to `foo-extra`.
	wsDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(wsDir, "foo-extra"), 0o755)) // sibling
	newPath := filepath.Join(wsDir, "abc-1--foo")
	require.NoError(t, os.MkdirAll(newPath, 0o755))

	oldEnc := EncodeCwdAsProjectDir(filepath.Join(wsDir, "foo"))
	siblingEnc := EncodeCwdAsProjectDir(filepath.Join(wsDir, "foo-extra"))
	root := claudeRig(t, oldEnc, siblingEnc, siblingEnc+"-internal")

	s := &Service{WorkspacesDir: wsDir, ClaudeProjectsDir: root}
	warnings := s.MoveSessionsForRename(filepath.Join(wsDir, "foo"), newPath)
	assert.Empty(t, warnings)

	// foo's dir moved
	_, err := os.Stat(filepath.Join(root, oldEnc))
	assert.True(t, os.IsNotExist(err))
	// sibling's dirs untouched
	_, err = os.Stat(filepath.Join(root, siblingEnc))
	assert.NoError(t, err)
	_, err = os.Stat(filepath.Join(root, siblingEnc+"-internal"))
	assert.NoError(t, err)
}

func TestMoveSessionsForRename_mergesWhenDestinationExists(t *testing.T) {
	wsDir := t.TempDir()
	newPath := filepath.Join(wsDir, "abc-1--feat")
	require.NoError(t, os.MkdirAll(newPath, 0o755))

	oldEnc := EncodeCwdAsProjectDir(filepath.Join(wsDir, "feat"))
	newEnc := EncodeCwdAsProjectDir(newPath)
	root := claudeRig(t, oldEnc)
	// User started a fresh session in the new path before attaching: dst
	// already exists with a different session.
	require.NoError(t, os.MkdirAll(filepath.Join(root, newEnc), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, newEnc, "fresh.jsonl"), []byte(`{}`), 0o600))

	s := &Service{WorkspacesDir: wsDir, ClaudeProjectsDir: root}
	warnings := s.MoveSessionsForRename(filepath.Join(wsDir, "feat"), newPath)
	assert.Empty(t, warnings, "merge with no overlap should be clean")

	entries, err := os.ReadDir(filepath.Join(root, newEnc))
	require.NoError(t, err)
	assert.Len(t, entries, 2, "both the carried-in and pre-existing jsonl should be present")
}

func TestMoveSessionsForRename_warnsOnFileCollision(t *testing.T) {
	wsDir := t.TempDir()
	newPath := filepath.Join(wsDir, "abc-1--feat")
	require.NoError(t, os.MkdirAll(newPath, 0o755))

	oldEnc := EncodeCwdAsProjectDir(filepath.Join(wsDir, "feat"))
	newEnc := EncodeCwdAsProjectDir(newPath)
	root := filepath.Join(t.TempDir(), "projects")
	require.NoError(t, os.MkdirAll(filepath.Join(root, oldEnc), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, oldEnc, "dup.jsonl"), []byte(`{"v":"old"}`), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(root, newEnc), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, newEnc, "dup.jsonl"), []byte(`{"v":"new"}`), 0o600))

	s := &Service{WorkspacesDir: wsDir, ClaudeProjectsDir: root}
	warnings := s.MoveSessionsForRename(filepath.Join(wsDir, "feat"), newPath)
	require.Len(t, warnings, 1)
	assert.Equal(t, "dup.jsonl", warnings[0].File)

	// Destination keeps its version
	data, err := os.ReadFile(filepath.Join(root, newEnc, "dup.jsonl"))
	require.NoError(t, err)
	assert.Equal(t, `{"v":"new"}`, string(data))
}

func TestMoveSessionsForRename_noopWhenClaudeProjectsDirEmpty(t *testing.T) {
	s := &Service{WorkspacesDir: t.TempDir()}
	assert.Nil(t, s.MoveSessionsForRename("/x", "/y"))
}

func TestMoveSessionsForRename_noopWhenProjectsDirMissing(t *testing.T) {
	s := &Service{
		WorkspacesDir:     t.TempDir(),
		ClaudeProjectsDir: filepath.Join(t.TempDir(), "does-not-exist"),
	}
	assert.Nil(t, s.MoveSessionsForRename("/x", "/y"))
}

func TestMoveSessionFile_movesFromAnyDirToTargetEncoded(t *testing.T) {
	wsDir := t.TempDir()
	target := filepath.Join(wsDir, "abc-1--feat")
	require.NoError(t, os.MkdirAll(target, 0o755))

	// Session originally created when the user was in `~/git/myorg` (parent dir).
	srcEnc := EncodeCwdAsProjectDir("/home/u/git/myorg")
	dstEnc := EncodeCwdAsProjectDir(target)
	root := filepath.Join(t.TempDir(), "projects")
	require.NoError(t, os.MkdirAll(filepath.Join(root, srcEnc), 0o755))
	id := "2bba4a38-93e1-4e9c-8921-faeb1c151189"
	require.NoError(t, os.WriteFile(filepath.Join(root, srcEnc, id+".jsonl"), []byte(`{}`), 0o600))

	s := &Service{WorkspacesDir: wsDir, ClaudeProjectsDir: root}
	src, dst, err := s.MoveSessionFile(t.Context(), id, target)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, srcEnc, id+".jsonl"), src)
	assert.Equal(t, filepath.Join(root, dstEnc, id+".jsonl"), dst)

	_, err = os.Stat(src)
	assert.True(t, os.IsNotExist(err))
	_, err = os.Stat(dst)
	assert.NoError(t, err)
}

func TestMoveSessionFile_errIfSessionMissing(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	require.NoError(t, os.MkdirAll(root, 0o755))
	s := &Service{WorkspacesDir: t.TempDir(), ClaudeProjectsDir: root}
	_, _, err := s.MoveSessionFile(t.Context(), "deadbeef", "/x")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestMoveSessionFile_errIfDestinationExists(t *testing.T) {
	wsDir := t.TempDir()
	target := filepath.Join(wsDir, "ws")
	require.NoError(t, os.MkdirAll(target, 0o755))

	srcEnc := EncodeCwdAsProjectDir("/home/u/git/myorg")
	dstEnc := EncodeCwdAsProjectDir(target)
	root := filepath.Join(t.TempDir(), "projects")
	require.NoError(t, os.MkdirAll(filepath.Join(root, srcEnc), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, dstEnc), 0o755))
	id := "sid"
	require.NoError(t, os.WriteFile(filepath.Join(root, srcEnc, id+".jsonl"), []byte(`a`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, dstEnc, id+".jsonl"), []byte(`b`), 0o600))

	s := &Service{WorkspacesDir: wsDir, ClaudeProjectsDir: root}
	_, _, err := s.MoveSessionFile(t.Context(), id, target)
	require.ErrorIs(t, err, ErrAlreadyExists)
}

func TestMoveSessionFile_noopIfAlreadyAtTarget(t *testing.T) {
	wsDir := t.TempDir()
	target := filepath.Join(wsDir, "ws")
	require.NoError(t, os.MkdirAll(target, 0o755))

	dstEnc := EncodeCwdAsProjectDir(target)
	root := filepath.Join(t.TempDir(), "projects")
	require.NoError(t, os.MkdirAll(filepath.Join(root, dstEnc), 0o755)) // only target dir
	id := "sid"
	original := []byte(`{"cwd":"x"}`)
	require.NoError(t, os.WriteFile(filepath.Join(root, dstEnc, id+".jsonl"), original, 0o600))

	s := &Service{WorkspacesDir: wsDir, ClaudeProjectsDir: root}
	src, dst, err := s.MoveSessionFile(t.Context(), id, target)
	require.NoError(t, err)
	assert.Equal(t, src, dst)
	data, _ := os.ReadFile(dst)
	assert.Equal(t, original, data)
}

func TestMoveSessionFile_errIfEmptySessionID(t *testing.T) {
	s := &Service{WorkspacesDir: t.TempDir(), ClaudeProjectsDir: t.TempDir()}
	_, _, err := s.MoveSessionFile(t.Context(), "", "/x")
	require.ErrorIs(t, err, ErrInvalidInput)
}

func TestMoveSessionFile_errIfClaudeProjectsDirUnset(t *testing.T) {
	s := &Service{WorkspacesDir: t.TempDir()}
	_, _, err := s.MoveSessionFile(t.Context(), "sid", "/x")
	require.ErrorIs(t, err, ErrInvalidInput)
}

func TestMoveSessionsForRename_nestedWorkspaceIsNotShadowedByItsProject(t *testing.T) {
	// A workspace nested inside a project shares its project's encoded
	// prefix. The sibling-collision guard must not read that as "these
	// session dirs belong to the project" and skip the move.
	wsDir := t.TempDir()
	proj := filepath.Join(wsDir, "q3-billing")
	require.NoError(t, os.MkdirAll(proj, 0o755))
	oldPath := filepath.Join(proj, "invoice")
	newPath := filepath.Join(proj, "abc-42--invoice")
	require.NoError(t, os.MkdirAll(newPath, 0o755))

	oldEnc := EncodeCwdAsProjectDir(oldPath)
	newEnc := EncodeCwdAsProjectDir(newPath)
	projEnc := EncodeCwdAsProjectDir(proj)
	root := claudeRig(t, oldEnc, oldEnc+"-core-api", projEnc)

	s := &Service{WorkspacesDir: wsDir, ClaudeProjectsDir: root}
	warnings := s.MoveSessionsForRename(oldPath, newPath)
	assert.Empty(t, warnings)

	// The workspace's own dirs moved, including the one for its worktree.
	assert.NoDirExists(t, filepath.Join(root, oldEnc))
	assert.DirExists(t, filepath.Join(root, newEnc))
	assert.NoDirExists(t, filepath.Join(root, oldEnc+"-core-api"))
	assert.DirExists(t, filepath.Join(root, newEnc+"-core-api"))

	// The containing project's own sessions are untouched.
	assert.DirExists(t, filepath.Join(root, projEnc))
}

func TestMoveSessionsForRename_nestedSiblingCollisionStillGuarded(t *testing.T) {
	// Within a project, `invoice` and `invoice-extra` coexist. The guard
	// still has to keep them apart.
	wsDir := t.TempDir()
	proj := filepath.Join(wsDir, "q3-billing")
	require.NoError(t, os.MkdirAll(filepath.Join(proj, "invoice-extra"), 0o755))
	oldPath := filepath.Join(proj, "invoice")
	newPath := filepath.Join(proj, "abc-42--invoice")
	require.NoError(t, os.MkdirAll(newPath, 0o755))

	oldEnc := EncodeCwdAsProjectDir(oldPath)
	siblingEnc := EncodeCwdAsProjectDir(filepath.Join(proj, "invoice-extra"))
	root := claudeRig(t, oldEnc, siblingEnc, siblingEnc+"-internal")

	s := &Service{WorkspacesDir: wsDir, ClaudeProjectsDir: root}
	warnings := s.MoveSessionsForRename(oldPath, newPath)
	assert.Empty(t, warnings)

	assert.NoDirExists(t, filepath.Join(root, oldEnc))
	assert.DirExists(t, filepath.Join(root, siblingEnc))
	assert.DirExists(t, filepath.Join(root, siblingEnc+"-internal"))
}

func TestMoveSessionsForRename_hyphenatedNameCollisionIsNotHijacked(t *testing.T) {
	// Claude's cwd encoding collapses both "/" and "." to "-", so the
	// nested path <ws>/p/foo and the top-level workspace <ws>/p-foo encode
	// to the same session dir. Renaming p/foo must not take that shared
	// dir with it — p-foo's sessions live there too — and the skip has to
	// be said out loud, since p/foo's own root sessions stay behind.
	wsDir := t.TempDir()
	proj := filepath.Join(wsDir, "p")
	require.NoError(t, os.MkdirAll(proj, 0o755))
	require.NoError(t, writeMeta(proj, Meta{Kind: KindProject}))
	oldPath := filepath.Join(proj, "foo")
	newPath := filepath.Join(proj, "abc-1--foo")
	require.NoError(t, os.MkdirAll(newPath, 0o755))
	require.NoError(t, writeMeta(newPath, Meta{Kind: KindTask}))
	// The colliding top-level workspace.
	require.NoError(t, os.MkdirAll(filepath.Join(wsDir, "p-foo"), 0o755))

	oldEnc := EncodeCwdAsProjectDir(oldPath) // == EncodeCwdAsProjectDir(<ws>/p-foo)
	require.Equal(t, oldEnc, EncodeCwdAsProjectDir(filepath.Join(wsDir, "p-foo")))
	root := claudeRig(t, oldEnc)

	s := &Service{WorkspacesDir: wsDir, ClaudeProjectsDir: root}
	warnings := s.MoveSessionsForRename(oldPath, newPath)

	// The shared dir stays where it is, and the skip is reported.
	assert.DirExists(t, filepath.Join(root, oldEnc))
	assert.NoDirExists(t, filepath.Join(root, EncodeCwdAsProjectDir(newPath)))
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0].Reason, "encodes to the same session dir")
}
