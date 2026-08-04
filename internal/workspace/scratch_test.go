package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mkScratch fills a workspace's claude_workspace/ with the given relative
// files (plus the generated .gitignore, which must not count as content).
func mkScratch(t *testing.T, wsPath string, files ...string) {
	t.Helper()
	dir := filepath.Join(wsPath, claudeWorkspaceDir)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*\n!.gitignore\n"), 0o644))
	for _, f := range files {
		p := filepath.Join(dir, filepath.FromSlash(f))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
	}
}

func TestService_Remove_scratchContentBlocks(t *testing.T) {
	wsDir := t.TempDir()
	ws := filepath.Join(wsDir, "abc-1--foo")
	mkTask(t, ws)
	mkScratch(t, ws, "notes.md", "design/plan.md")

	svc := projectSvc(t, wsDir, &fakeInspector{})

	_, err := svc.Remove(context.Background(), RemoveOptions{Name: "abc-1--foo"})
	var scratch *ErrScratchNotEmpty
	require.True(t, errors.As(err, &scratch), "got %v", err)
	require.Len(t, scratch.Contents, 1)
	assert.Equal(t, "abc-1--foo", scratch.Contents[0].Ref)
	assert.Equal(t, []string{"design/plan.md", "notes.md"}, scratch.Contents[0].Files,
		"sorted, .gitignore excluded")
	assert.DirExists(t, ws, "refusal must precede any deletion")
}

func TestService_Remove_scratchGitignoreAloneDoesNotBlock(t *testing.T) {
	wsDir := t.TempDir()
	ws := filepath.Join(wsDir, "abc-1--foo")
	mkTask(t, ws)
	mkScratch(t, ws) // .gitignore only

	svc := projectSvc(t, wsDir, &fakeInspector{})

	_, err := svc.Remove(context.Background(), RemoveOptions{Name: "abc-1--foo"})
	require.NoError(t, err)
	assert.NoDirExists(t, ws)
}

func TestService_Remove_scratchClearedByDeleteScratchAndForce(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts RemoveOptions
	}{
		{"DeleteScratch", RemoveOptions{Name: "abc-1--foo", DeleteScratch: true}},
		{"Force", RemoveOptions{Name: "abc-1--foo", Force: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wsDir := t.TempDir()
			ws := filepath.Join(wsDir, "abc-1--foo")
			mkTask(t, ws)
			mkScratch(t, ws, "notes.md")

			svc := projectSvc(t, wsDir, &fakeInspector{})

			_, err := svc.Remove(context.Background(), tc.opts)
			require.NoError(t, err)
			assert.NoDirExists(t, ws)
		})
	}
}

func TestService_Remove_scratchOfNestedWorkspacesCounts(t *testing.T) {
	wsDir := t.TempDir()
	proj := filepath.Join(wsDir, "q3")
	mkProject(t, proj)
	child := filepath.Join(proj, "abc-2--bar")
	mkTask(t, child)
	mkScratch(t, child, "findings.md")

	svc := projectSvc(t, wsDir, &fakeInspector{})

	// Without --recursive the nested-workspace refusal wins; the scratch
	// check must not fire first for content the removal would not reach yet.
	_, err := svc.Remove(context.Background(), RemoveOptions{Name: "q3"})
	var notEmpty *ErrNotEmpty
	require.True(t, errors.As(err, &notEmpty), "got %v", err)

	_, err = svc.Remove(context.Background(), RemoveOptions{Name: "q3", Recursive: true})
	var scratch *ErrScratchNotEmpty
	require.True(t, errors.As(err, &scratch), "got %v", err)
	require.Len(t, scratch.Contents, 1)
	assert.Equal(t, "q3/abc-2--bar", scratch.Contents[0].Ref)
	assert.Equal(t, []string{"findings.md"}, scratch.Contents[0].Files)
}
