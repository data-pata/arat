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

// mkBrokenMarker creates a workspace dir whose marker file exists but cannot
// be classified (unknown kind), the fault a newer arat or a stray edit leaves
// behind.
func mkBrokenMarker(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(path, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(path, MetaFile), []byte("kind = \"epic\"\n"), 0o644))
}

func TestService_List_markerFaultIsScopedToOneWorkspace(t *testing.T) {
	wsDir := t.TempDir()

	mkTask(t, filepath.Join(wsDir, "one"))
	broken := filepath.Join(wsDir, "two")
	mkBrokenMarker(t, broken)
	// A valid child inside the broken workspace: classification of subdirs is
	// marker-driven and must survive the parent's fault.
	mkTask(t, filepath.Join(broken, "abc-9--child"))
	// A legacy workspace with no marker at all stays a plain task.
	require.NoError(t, os.MkdirAll(filepath.Join(wsDir, "three"), 0o755))

	svc := projectSvc(t, wsDir, &fakeInspector{})

	items, err := svc.List(context.Background(), ListOptions{Detail: DetailLight})
	require.NoError(t, err, "one broken marker must not fail the tree walk")
	require.Len(t, items, 3)

	byName := map[string]Workspace{}
	for _, ws := range items {
		byName[ws.Name] = ws
	}
	assert.Empty(t, byName["one"].MetaError)
	assert.Empty(t, byName["three"].MetaError)

	two := byName["two"]
	assert.Contains(t, two.MetaError, "epic")
	assert.Equal(t, KindTask, two.Kind, "a broken marker degrades to the task default")
	require.Len(t, two.Children, 1, "children of a broken workspace still hydrate")
	assert.Equal(t, "two/abc-9--child", two.Children[0].Ref)
}

func TestService_Get_worksAroundBrokenSibling(t *testing.T) {
	wsDir := t.TempDir()
	mkTask(t, filepath.Join(wsDir, "one"))
	mkBrokenMarker(t, filepath.Join(wsDir, "two"))

	svc := projectSvc(t, wsDir, &fakeInspector{})

	ws, err := svc.Get(context.Background(), "one")
	require.NoError(t, err, "an unrelated workspace must resolve despite the broken one")
	assert.Empty(t, ws.MetaError)

	got, err := svc.Get(context.Background(), "two")
	require.NoError(t, err, "the broken workspace itself must still resolve (rm depends on it)")
	assert.NotEmpty(t, got.MetaError)
}

func TestService_Remove_worksOnBrokenMarker(t *testing.T) {
	wsDir := t.TempDir()
	broken := filepath.Join(wsDir, "two")
	mkBrokenMarker(t, broken)

	svc := projectSvc(t, wsDir, &fakeInspector{})

	res, err := svc.Remove(context.Background(), RemoveOptions{Name: "two"})
	require.NoError(t, err, "rm is the repair path for a broken marker and must not be blocked by it")
	assert.Equal(t, []string{"two"}, res.Removed)
	assert.NoDirExists(t, broken)
}

func TestService_structuralOpsRefuseBrokenMarker(t *testing.T) {
	newSvc := func(t *testing.T) *Service {
		t.Helper()
		wsDir := t.TempDir()
		mkBrokenMarker(t, filepath.Join(wsDir, "broken"))
		return projectSvc(t, wsDir, &fakeInspector{})
	}
	ctx := context.Background()

	tests := []struct {
		name string
		op   func(svc *Service) error
	}{
		{"new inside broken parent", func(svc *Service) error {
			_, err := svc.New(ctx, NewOptions{ShortName: "child", Parent: "broken"})
			return err
		}},
		{"attach ticket", func(svc *Service) error {
			_, err := svc.AttachTicket(ctx, AttachOptions{Name: "broken", Ticket: "abc-1"})
			return err
		}},
		{"link linear", func(svc *Service) error {
			_, err := svc.LinkLinear(ctx, LinkOptions{Ref: "broken", Linear: LinearRef{Kind: LinearKindProject, ID: "x"}})
			return err
		}},
		{"unlink linear", func(svc *Service) error {
			_, err := svc.UnlinkLinear(ctx, "broken")
			return err
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.op(newSvc(t))
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrInvalidInput), "got %v", err)
			assert.Contains(t, err.Error(), "unreadable workspace marker")
		})
	}
}

func TestService_ProjectAt_failsLoudlyOnBrokenMarker(t *testing.T) {
	wsDir := t.TempDir()
	broken := filepath.Join(wsDir, "broken")
	mkBrokenMarker(t, broken)

	svc := projectSvc(t, wsDir, &fakeInspector{})

	_, err := svc.ProjectAt(context.Background(), broken)
	require.Error(t, err, "inferring 'no project' from an unreadable marker would misplace `arat new`")
	assert.True(t, errors.Is(err, ErrInvalidInput), "got %v", err)
}
