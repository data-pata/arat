package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/data-pata/arat/internal/git"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mkProject creates a project workspace dir (marker file included) at path.
func mkProject(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(path, 0o755))
	require.NoError(t, writeMeta(path, Meta{Kind: KindProject}))
}

// mkTask creates a task workspace dir with the marker file at path.
func mkTask(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(path, 0o755))
	require.NoError(t, writeMeta(path, Meta{Kind: KindTask}))
}

func projectSvc(t *testing.T, wsDir string, insp *fakeInspector) *Service {
	t.Helper()
	svc, err := NewService(ServiceOptions{
		Root:          t.TempDir(),
		WorkspacesDir: wsDir,
		BranchPrefix:  "ps",
		TicketRE:      regexp.MustCompile(`^[a-z]+-[0-9]+$`),
		TicketURL:     "https://linear.app/o/issue/{TICKET_UPPER}",
		Git:           insp,
	})
	require.NoError(t, err)
	return svc
}

func TestMeta_roundTrip(t *testing.T) {
	dir := t.TempDir()

	// A directory with no marker reads as "not a workspace marker at all",
	// which callers treat as a task workspace.
	m, err := readMeta(dir)
	require.NoError(t, err)
	assert.Nil(t, m)
	assert.False(t, hasMeta(dir))

	ref := LinearRef{Kind: LinearKindInitiative, ID: "abc123", Name: "Payments 2026", URL: "https://linear.app/o/initiative/abc123"}
	require.NoError(t, writeMeta(dir, Meta{Kind: KindProject, Linear: &ref}))
	assert.True(t, hasMeta(dir))

	got, err := readMeta(dir)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, KindProject, got.Kind)
	require.NotNil(t, got.Linear)
	assert.Equal(t, ref, *got.Linear)
}

func TestMeta_malformedIsAnError(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, MetaFile), []byte("kind = \"nonsense\"\n"), 0o644))

	_, err := readMeta(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nonsense")
}

func TestService_List_nestedTree(t *testing.T) {
	wsDir := t.TempDir()

	// q3-billing (project, own worktree)
	//   abc-12--invoice (task, one worktree)
	//   dunning (project)
	//     abc-20--retry (task, one worktree)
	// standalone (legacy task, no marker file)
	proj := filepath.Join(wsDir, "q3-billing")
	mkProject(t, proj)
	require.NoError(t, os.MkdirAll(filepath.Join(proj, "core-api"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(proj, claudeWorkspaceDir), 0o755))

	invoice := filepath.Join(proj, "abc-12--invoice")
	mkTask(t, invoice)
	require.NoError(t, os.MkdirAll(filepath.Join(invoice, "core-api"), 0o755))

	dunning := filepath.Join(proj, "dunning")
	mkProject(t, dunning)

	retry := filepath.Join(dunning, "abc-20--retry")
	mkTask(t, retry)
	require.NoError(t, os.MkdirAll(filepath.Join(retry, "core-api"), 0o755))

	standalone := filepath.Join(wsDir, "standalone")
	require.NoError(t, os.MkdirAll(filepath.Join(standalone, "web"), 0o755))

	insp := &fakeInspector{
		worktrees: map[string]bool{
			filepath.Join(proj, "core-api"):    true,
			filepath.Join(invoice, "core-api"): true,
			filepath.Join(retry, "core-api"):   true,
			filepath.Join(standalone, "web"):   true,
		},
		insp: map[string]git.Inspection{
			filepath.Join(proj, "core-api"):    {Branch: "ps--q3-billing"},
			filepath.Join(invoice, "core-api"): {Branch: "ps--invoice--abc-12", Dirty: true},
			filepath.Join(retry, "core-api"):   {Branch: "ps--retry--abc-20"},
			filepath.Join(standalone, "web"):   {Branch: "ps--standalone"},
		},
	}
	svc := projectSvc(t, wsDir, insp)

	items, err := svc.List(context.Background(), ListOptions{})
	require.NoError(t, err)
	require.Len(t, items, 2, "only top-level workspaces are returned")

	billing := items[0]
	assert.Equal(t, "q3-billing", billing.Ref)
	assert.Equal(t, KindProject, billing.Kind)
	assert.Empty(t, billing.Parent)
	// The project's own worktree is a repo, not a child.
	require.Len(t, billing.Repos, 1)
	assert.Equal(t, "core-api", billing.Repos[0].Name)
	require.Len(t, billing.Children, 2)

	// Children are sorted by name: abc-12--invoice before dunning.
	child := billing.Children[0]
	assert.Equal(t, "q3-billing/abc-12--invoice", child.Ref)
	assert.Equal(t, "q3-billing", child.Parent)
	assert.Equal(t, KindTask, child.Kind)
	assert.Equal(t, "abc-12", child.Ticket)
	assert.Equal(t, "https://linear.app/o/issue/ABC-12", child.TicketURL)
	require.Len(t, child.Repos, 1)
	assert.True(t, child.Repos[0].Dirty)

	sub := billing.Children[1]
	assert.Equal(t, "q3-billing/dunning", sub.Ref)
	assert.Equal(t, KindProject, sub.Kind)
	require.Len(t, sub.Children, 1)
	assert.Equal(t, "q3-billing/dunning/abc-20--retry", sub.Children[0].Ref)

	// A directory with no marker at the top level is still a task workspace,
	// so workspaces created before projects existed keep working.
	assert.Equal(t, "standalone", items[1].Ref)
	assert.Equal(t, KindTask, items[1].Kind)
	require.Len(t, items[1].Repos, 1)

	// Flatten reaches every workspace regardless of depth.
	all := Flatten(items)
	refs := make([]string, len(all))
	for i, ws := range all {
		refs[i] = ws.Ref
	}
	assert.Equal(t, []string{
		"q3-billing",
		"q3-billing/abc-12--invoice",
		"q3-billing/dunning",
		"q3-billing/dunning/abc-20--retry",
		"standalone",
	}, refs)
}

func TestService_Get_byRefAndByBareName(t *testing.T) {
	wsDir := t.TempDir()
	proj := filepath.Join(wsDir, "q3-billing")
	mkProject(t, proj)
	mkTask(t, filepath.Join(proj, "abc-12--invoice"))

	svc := projectSvc(t, wsDir, &fakeInspector{})
	ctx := context.Background()

	byRef, err := svc.Get(ctx, "q3-billing/abc-12--invoice")
	require.NoError(t, err)
	assert.Equal(t, "q3-billing/abc-12--invoice", byRef.Ref)

	// A bare name that is unique in the tree resolves without the full path.
	byName, err := svc.Get(ctx, "abc-12--invoice")
	require.NoError(t, err)
	assert.Equal(t, "q3-billing/abc-12--invoice", byName.Ref)

	_, err = svc.Get(ctx, "nope")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestService_Get_ambiguousBareName(t *testing.T) {
	wsDir := t.TempDir()
	a := filepath.Join(wsDir, "proj-a")
	b := filepath.Join(wsDir, "proj-b")
	mkProject(t, a)
	mkProject(t, b)
	mkTask(t, filepath.Join(a, "abc-1--fix"))
	mkTask(t, filepath.Join(b, "abc-1--fix"))

	svc := projectSvc(t, wsDir, &fakeInspector{})

	_, err := svc.Get(context.Background(), "abc-1--fix")
	var amb *ErrAmbiguous
	require.ErrorAs(t, err, &amb)
	assert.Equal(t, []string{"proj-a/abc-1--fix", "proj-b/abc-1--fix"}, amb.Matches)

	// The full ref is never ambiguous.
	ws, err := svc.Get(context.Background(), "proj-b/abc-1--fix")
	require.NoError(t, err)
	assert.Equal(t, "proj-b/abc-1--fix", ws.Ref)
}

func TestService_Get_rejectsEscapingRef(t *testing.T) {
	wsDir := t.TempDir()
	svc := projectSvc(t, wsDir, &fakeInspector{})

	_, err := svc.Get(context.Background(), "../outside")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNotFound, "an escaping ref must not resolve to a directory outside workspaces_dir")
}

func TestService_WorkspaceAt(t *testing.T) {
	wsDir := t.TempDir()
	proj := filepath.Join(wsDir, "q3-billing")
	mkProject(t, proj)
	require.NoError(t, os.MkdirAll(filepath.Join(proj, "core-api"), 0o755))
	invoice := filepath.Join(proj, "abc-12--invoice")
	mkTask(t, invoice)
	require.NoError(t, os.MkdirAll(filepath.Join(invoice, "core-api"), 0o755))

	svc := projectSvc(t, wsDir, &fakeInspector{})
	ctx := context.Background()

	tests := []struct {
		name    string
		cwd     string
		wantRef string
	}{
		{"inside a nested workspace", invoice, "q3-billing/abc-12--invoice"},
		{"inside a nested workspace's worktree", filepath.Join(invoice, "core-api"), "q3-billing/abc-12--invoice"},
		{"inside the project itself", proj, "q3-billing"},
		{"inside the project's own worktree", filepath.Join(proj, "core-api"), "q3-billing"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ws, err := svc.WorkspaceAt(ctx, tc.cwd)
			require.NoError(t, err)
			assert.Equal(t, tc.wantRef, ws.Ref)
		})
	}

	_, err := svc.WorkspaceAt(ctx, t.TempDir())
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestService_ProjectAt(t *testing.T) {
	wsDir := t.TempDir()
	proj := filepath.Join(wsDir, "q3-billing")
	mkProject(t, proj)
	invoice := filepath.Join(proj, "abc-12--invoice")
	mkTask(t, invoice)
	solo := filepath.Join(wsDir, "solo")
	mkTask(t, solo)

	svc := projectSvc(t, wsDir, &fakeInspector{})
	ctx := context.Background()

	// Standing in a task inside a project resolves to the project. Tasks can
	// hold children, but working in one is the ordinary case, so plain
	// `arat new` there means a sibling rather than a sub-issue.
	got, err := svc.ProjectAt(ctx, invoice)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "q3-billing", got.Ref)

	// Standing in the project resolves to itself.
	got, err = svc.ProjectAt(ctx, proj)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "q3-billing", got.Ref)

	// A top-level task workspace is inside no project.
	got, err = svc.ProjectAt(ctx, solo)
	require.NoError(t, err)
	assert.Nil(t, got)
}

// Since tasks nest, the walk up from a sub-issue passes through another task
// before reaching the project. Stopping at the immediate parent would return
// a task as if it were the project.
func TestService_ProjectAt_walksPastNestedTasks(t *testing.T) {
	wsDir := t.TempDir()
	proj := filepath.Join(wsDir, "q3-billing")
	mkProject(t, proj)
	invoice := filepath.Join(proj, "abc-12--invoice")
	mkTask(t, invoice)
	fonts := filepath.Join(invoice, "abc-18--fonts")
	mkTask(t, fonts)
	kerning := filepath.Join(fonts, "abc-19--kerning")
	mkTask(t, kerning)

	svc := projectSvc(t, wsDir, &fakeInspector{})
	ctx := context.Background()

	got, err := svc.ProjectAt(ctx, kerning)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "q3-billing", got.Ref)

	// The same chain outside any project bottoms out at nil rather than
	// returning the top-level task.
	solo := filepath.Join(wsDir, "solo")
	mkTask(t, solo)
	sub := filepath.Join(solo, "sub")
	mkTask(t, sub)

	got, err = svc.ProjectAt(ctx, sub)
	require.NoError(t, err)
	assert.Nil(t, got)
}

// A task workspace hydrates its sub-issues the same way a project hydrates
// its issues, at every depth and with its own worktrees intact.
func TestService_List_nestedTasks(t *testing.T) {
	wsDir := t.TempDir()
	invoice := filepath.Join(wsDir, "abc-12--invoice")
	mkTask(t, invoice)
	require.NoError(t, os.MkdirAll(filepath.Join(invoice, "core-api"), 0o755))
	fonts := filepath.Join(invoice, "abc-18--fonts")
	mkTask(t, fonts)
	kerning := filepath.Join(fonts, "abc-19--kerning")
	mkTask(t, kerning)

	svc := projectSvc(t, wsDir, &fakeInspector{worktrees: map[string]bool{
		filepath.Join(invoice, "core-api"): true,
	}})
	ctx := context.Background()

	items, err := svc.List(ctx, ListOptions{})
	require.NoError(t, err)
	require.Len(t, items, 1)

	top := items[0]
	assert.Equal(t, KindTask, top.Kind)
	assert.Equal(t, "abc-12", top.Ticket)
	require.Len(t, top.Repos, 1, "the task's own worktree survives having children")
	assert.Equal(t, "core-api", top.Repos[0].Name)

	require.Len(t, top.Children, 1)
	assert.Equal(t, "abc-12--invoice/abc-18--fonts", top.Children[0].Ref)
	require.Len(t, top.Children[0].Children, 1)
	assert.Equal(t, "abc-12--invoice/abc-18--fonts/abc-19--kerning", top.Children[0].Children[0].Ref)

	// Flatten and ref lookup reach every depth.
	assert.Len(t, Flatten(items), 3)
	got, err := svc.Get(ctx, "abc-19--kerning")
	require.NoError(t, err)
	assert.Equal(t, "abc-12--invoice/abc-18--fonts/abc-19--kerning", got.Ref)
}

func TestService_LinkLinear(t *testing.T) {
	wsDir := t.TempDir()
	proj := filepath.Join(wsDir, "q3-billing")
	mkProject(t, proj)
	task := filepath.Join(wsDir, "abc-1--solo")
	mkTask(t, task)

	svc := projectSvc(t, wsDir, &fakeInspector{})
	ctx := context.Background()

	ref := LinearRef{Kind: LinearKindProject, ID: "slug-1", Name: "Q3 Billing", URL: "https://linear.app/o/project/slug-1"}
	ws, err := svc.LinkLinear(ctx, LinkOptions{Ref: "q3-billing", Linear: ref})
	require.NoError(t, err)
	require.NotNil(t, ws.Linear)
	assert.Equal(t, ref, *ws.Linear)

	// The link survives a re-read from disk.
	reloaded, err := svc.Get(ctx, "q3-billing")
	require.NoError(t, err)
	require.NotNil(t, reloaded.Linear)
	assert.Equal(t, ref, *reloaded.Linear)

	// Unlinking clears it, and is idempotent.
	_, err = svc.UnlinkLinear(ctx, "q3-billing")
	require.NoError(t, err)
	reloaded, err = svc.Get(ctx, "q3-billing")
	require.NoError(t, err)
	assert.Nil(t, reloaded.Linear)
	_, err = svc.UnlinkLinear(ctx, "q3-billing")
	assert.NoError(t, err, "unlinking an unlinked project is a no-op")

	// A task workspace attaches an issue, not a Linear project.
	_, err = svc.LinkLinear(ctx, LinkOptions{Ref: "abc-1--solo", Linear: ref})
	assert.ErrorIs(t, err, ErrInvalidInput)

	// The kind is validated before anything is written.
	_, err = svc.LinkLinear(ctx, LinkOptions{Ref: "q3-billing", Linear: LinearRef{Kind: "epic", ID: "x"}})
	assert.ErrorIs(t, err, ErrInvalidInput)
}

// A top-level workspace's ref is indistinguishable from a bare name, so a
// same-named nested workspace makes the query ambiguous — silently preferring
// the top-level one would let `arat rm fix` delete it while the user meant
// proj/fix. The anchored ./ form is the escape hatch that pins the top-level
// workspace, whose ref no other query can reach.
func TestResolve_topLevelDoesNotShadowNestedName(t *testing.T) {
	items := []Workspace{
		{Name: "fix", Ref: "fix"},
		{Name: "proj", Ref: "proj", Kind: KindProject, Children: []Workspace{
			{Name: "fix", Ref: "proj/fix", Parent: "proj"},
		}},
	}

	_, err := Resolve(items, "fix")
	var amb *ErrAmbiguous
	require.ErrorAs(t, err, &amb)
	assert.Equal(t, []string{"fix", "proj/fix"}, amb.Matches)
	assert.Contains(t, amb.Error(), "./fix", "the error must name the working escape hatch")

	got, err := Resolve(items, "./fix")
	require.NoError(t, err)
	assert.Equal(t, "fix", got.Ref)

	got, err = Resolve(items, "proj/fix")
	require.NoError(t, err)
	assert.Equal(t, "proj/fix", got.Ref)

	_, err = Resolve(items, "missing")
	assert.True(t, errors.Is(err, ErrNotFound))
}

// A failed full ref whose last segment names a real workspace gets pointed at
// the actual ref instead of a bare not-found.
func TestResolve_didYouMeanOnWrongMiddleSegment(t *testing.T) {
	items := []Workspace{
		{Name: "proj", Ref: "proj", Kind: KindProject, Children: []Workspace{
			{Name: "abc-12--pdf", Ref: "proj/abc-12--pdf", Parent: "proj", Children: []Workspace{
				{Name: "abc-18--fonts", Ref: "proj/abc-12--pdf/abc-18--fonts", Parent: "proj/abc-12--pdf"},
			}},
		}},
	}

	_, err := Resolve(items, "proj/abc-18--fonts")
	require.ErrorIs(t, err, ErrNotFound)
	assert.Contains(t, err.Error(), "did you mean proj/abc-12--pdf/abc-18--fonts?")
}
