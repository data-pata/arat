package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The generated CLAUDE.md and .code-workspace are derived artifacts keyed to
// the workspace's repo set and name; every mutation that changes those must
// refresh them, or the one consumer they exist for reads stale context.

func TestAddRepos_refreshesClaudeMDReposLine(t *testing.T) {
	root := setupRoot(t, "repo-a", "repo-b")
	svc := newSvc(t, root)
	ctx := t.Context()

	ws, err := svc.New(ctx, NewOptions{ShortName: "grow", Repos: []string{"repo-a"}})
	require.NoError(t, err)
	assertFileContains(t, filepath.Join(ws.Path, "CLAUDE.md"), "**Repos**: repo-a\n")

	_, err = svc.AddRepos(ctx, AddReposOptions{Workspace: "grow", Repos: []string{"repo-b"}})
	require.NoError(t, err)
	assertFileContains(t, filepath.Join(ws.Path, "CLAUDE.md"), "**Repos**: repo-a repo-b")
}

func TestAddRepos_missingReposLineIsNotAnError(t *testing.T) {
	root := setupRoot(t, "repo-a", "repo-b")
	svc := newSvc(t, root)
	ctx := t.Context()

	ws, err := svc.New(ctx, NewOptions{ShortName: "rewritten", Repos: []string{"repo-a"}})
	require.NoError(t, err)
	// The user owns the file; a rewritten header without the generated line
	// must not block adding repos.
	require.NoError(t, os.WriteFile(filepath.Join(ws.Path, "CLAUDE.md"), []byte("# mine\n\n## Notes\n"), 0o644))

	_, err = svc.AddRepos(ctx, AddReposOptions{Workspace: "rewritten", Repos: []string{"repo-b"}})
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(ws.Path, "CLAUDE.md"))
	require.NoError(t, err)
	assert.Equal(t, "# mine\n\n## Notes\n", string(data), "a header without the line is left alone")
}

func TestAttachTicket_renamesCodeWorkspace(t *testing.T) {
	root := setupRoot(t, "repo-a")
	svc := newSvc(t, root)
	ctx := t.Context()

	ws, err := svc.New(ctx, NewOptions{ShortName: "fix", Repos: []string{"repo-a"}, GenerateCodeWorkspace: true})
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(ws.Path, "fix.code-workspace"))

	res, err := svc.AttachTicket(ctx, AttachOptions{Name: "fix", Ticket: "abc-7"})
	require.NoError(t, err)
	assert.Empty(t, res.Warnings)
	newPath := res.Workspace.Path
	assert.FileExists(t, filepath.Join(newPath, "abc-7--fix.code-workspace"),
		"the code-workspace file is keyed by the directory name and must follow the rename")
	assert.NoFileExists(t, filepath.Join(newPath, "fix.code-workspace"))

	// The rename is what keeps `repo add`'s regeneration finding the file.
	_, err = svc.AddRepos(ctx, AddReposOptions{Workspace: "abc-7--fix", Repos: []string{"repo-a"}})
	require.Error(t, err, "already present — but the lookup happened under the new name")
}
