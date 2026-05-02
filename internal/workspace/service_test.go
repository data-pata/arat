package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeInspector implements the workspace.Git interface for List-style tests
// (read-only). New/Remove tests use real git in t.TempDir() instead.
type fakeInspector struct {
	worktrees map[string]bool
	insp      map[string]Inspection
	canonical map[string]string
}

func (f *fakeInspector) IsWorktree(_ context.Context, dir string) bool { return f.worktrees[dir] }
func (f *fakeInspector) Inspect(_ context.Context, dir string) (Inspection, error) {
	if i, ok := f.insp[dir]; ok {
		return i, nil
	}
	return Inspection{}, nil
}
func (f *fakeInspector) CanonicalRepoName(_ context.Context, dir string) string {
	return f.canonical[dir]
}
func (*fakeInspector) CanonicalRepoPath(context.Context, string) string { return "" }
func (*fakeInspector) Fetch(context.Context, string) error              { panic("unused in List tests") }
func (*fakeInspector) WorktreeAdd(context.Context, string, string, string, string) error {
	panic("unused in List tests")
}
func (*fakeInspector) WorktreeRemove(context.Context, string, string, bool) error {
	panic("unused in List tests")
}
func (*fakeInspector) BranchDelete(context.Context, string, string, bool) error {
	panic("unused in List tests")
}
func (*fakeInspector) BranchRename(context.Context, string, string, string) error {
	panic("unused in List tests")
}
func (*fakeInspector) WorktreeRepair(context.Context, string) error {
	panic("unused in List tests")
}

func TestService_List(t *testing.T) {
	wsDir := t.TempDir()

	// abc-1--foo with two worktree-shaped subdirs and one stray file
	wa := filepath.Join(wsDir, "abc-1--foo")
	require.NoError(t, os.MkdirAll(filepath.Join(wa, "core-app"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(wa, "ui-app"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(wa, "claude_workspace"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(wa, "CLAUDE.md"), []byte("x"), 0o644))

	// bar — no ticket
	wb := filepath.Join(wsDir, "bar")
	require.NoError(t, os.MkdirAll(filepath.Join(wb, "core-app"), 0o755))

	// also a non-dir entry at wsDir top level (should be ignored)
	require.NoError(t, os.WriteFile(filepath.Join(wsDir, "stray.txt"), []byte("x"), 0o644))

	insp := &fakeInspector{
		worktrees: map[string]bool{
			filepath.Join(wa, "core-app"): true,
			filepath.Join(wa, "ui-app"):        true,
			filepath.Join(wb, "core-app"): true,
		},
		insp: map[string]Inspection{
			filepath.Join(wa, "core-app"): {Branch: "ps--foo--abc-1", Dirty: true},
			filepath.Join(wa, "ui-app"):        {Branch: "ps--foo--abc-1", Unpushed: true, Stashes: 2},
			filepath.Join(wb, "core-app"): {Branch: "ps--bar"},
		},
	}
	svc := &Service{
		WorkspacesDir: wsDir,
		TicketRE:      regexp.MustCompile(`^[a-z]+-[0-9]+$`),
		TicketURL:     "https://linear.app/x/issue/{TICKET_UPPER}",
		Git: insp,
	}

	got, err := svc.List(t.Context())
	require.NoError(t, err)
	require.Len(t, got, 2)

	// Sorted alphabetically: abc-1--foo, bar
	assert.Equal(t, "abc-1--foo", got[0].Name)
	assert.Equal(t, "abc-1", got[0].Ticket)
	assert.Equal(t, "foo", got[0].ShortName)
	assert.Equal(t, "https://linear.app/x/issue/ABC-1", got[0].TicketURL)
	require.Len(t, got[0].Repos, 2)

	repos := map[string]RepoStatus{}
	for _, r := range got[0].Repos {
		repos[r.Name] = r
	}
	assert.True(t, repos["core-app"].Dirty)
	assert.False(t, repos["core-app"].Unpushed)
	assert.True(t, repos["ui-app"].Unpushed)
	assert.Equal(t, 2, repos["ui-app"].Stashes)

	assert.Equal(t, "bar", got[1].Name)
	assert.Equal(t, "", got[1].Ticket)
	assert.Equal(t, "bar", got[1].ShortName)
	assert.Empty(t, got[1].TicketURL)
	require.Len(t, got[1].Repos, 1)
	assert.Equal(t, "core-app", got[1].Repos[0].Name)
	assert.Equal(t, "ps--bar", got[1].Repos[0].Branch)
}

func TestService_List_singleRepoWorkspace(t *testing.T) {
	wsDir := t.TempDir()

	// "selfwt" — workspace dir is itself a worktree (has .git);
	// its top-level subdirs are NOT separate worktrees.
	wa := filepath.Join(wsDir, "selfwt")
	require.NoError(t, os.MkdirAll(filepath.Join(wa, "build"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(wa, "docs"), 0o755))

	insp := &fakeInspector{
		worktrees: map[string]bool{
			wa: true, // workspace dir itself is a worktree
			// build/docs intentionally NOT marked as worktrees
		},
		insp: map[string]Inspection{
			wa: {Branch: "ps--selfwt", Dirty: true, Stashes: 5},
		},
		canonical: map[string]string{
			wa: "core-app",
		},
	}
	svc := &Service{
		WorkspacesDir: wsDir,
		TicketRE:      regexp.MustCompile(`^[a-z]+-[0-9]+$`),
		Git: insp,
	}
	got, err := svc.List(t.Context())
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Len(t, got[0].Repos, 1, "single-repo workspace should produce exactly one repo entry, not one per top-level subdir")
	assert.Equal(t, "core-app", got[0].Repos[0].Name)
	assert.Equal(t, "ps--selfwt", got[0].Repos[0].Branch)
	assert.True(t, got[0].Repos[0].Dirty)
	assert.Equal(t, 5, got[0].Repos[0].Stashes)
}

func TestService_List_missingDir(t *testing.T) {
	svc := &Service{
		WorkspacesDir: filepath.Join(t.TempDir(), "nope"),
		TicketRE:      regexp.MustCompile(`^[a-z]+-[0-9]+$`),
		Git:           &fakeInspector{},
	}
	_, err := svc.List(t.Context())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNoWorkspacesDir))
}

func TestParseName(t *testing.T) {
	re := regexp.MustCompile(`^[a-z]+-[0-9]+$`)
	tests := []struct {
		in           string
		wantTicket   string
		wantShort    string
	}{
		{"foo", "", "foo"},
		{"abc-1--foo", "abc-1", "foo"},
		{"ABC-1--foo", "abc-1", "foo"}, // ticket lowercased
		{"foo--bar", "", "foo--bar"},   // left side doesn't match regex; whole name is short
		{"--foo", "", "--foo"},          // leading "--" yields empty left → keep whole name
		{"abc-1--foo--bar", "abc-1", "foo--bar"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			ticket, short := ParseName(tt.in, re)
			assert.Equal(t, tt.wantTicket, ticket)
			assert.Equal(t, tt.wantShort, short)
		})
	}
}

func TestBranchAndDirName(t *testing.T) {
	assert.Equal(t, "ps--foo", BranchName("ps", "foo", ""))
	assert.Equal(t, "ps--foo--abc-1", BranchName("ps", "foo", "abc-1"))
	assert.Equal(t, "foo", DirName("foo", ""))
	assert.Equal(t, "abc-1--foo", DirName("foo", "abc-1"))
}

func TestRenderTicketURL(t *testing.T) {
	assert.Equal(t, "https://linear.app/x/issue/ABC-1",
		renderTicketURL("https://linear.app/x/issue/{TICKET_UPPER}", "abc-1"))
	assert.Equal(t, "x/abc-1/ABC-1",
		renderTicketURL("x/{TICKET}/{TICKET_UPPER}", "abc-1"))
}
