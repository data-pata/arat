package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WorkspaceAt returns the workspace containing dir: the deepest workspace
// whose directory is dir itself or an ancestor of it.
//
// Deepest wins. Standing in a workspace nested inside a project resolves to
// that workspace, not to the project above it, which is what commands like
// `arat note` and `arat repo add` need — they act on the work you are
// currently in.
//
// A directory counts as a workspace when it sits directly under
// workspaces_dir (every top-level directory there is one, including those
// created before the marker file existed) or when it carries the marker file.
// Repo worktrees never carry the marker, so standing inside one resolves to
// the workspace that owns it.
//
// Returns ErrNotFound when dir is outside workspaces_dir.
func (s *Service) WorkspaceAt(ctx context.Context, dir string) (*Workspace, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	wsAbs, err := filepath.Abs(s.WorkspacesDir)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(wsAbs, abs)
	if err != nil {
		return nil, err
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return nil, fmt.Errorf("%w: not inside a workspace (%s is not under workspaces_dir %s)", ErrNotFound, abs, wsAbs)
	}

	segs := strings.Split(rel, string(os.PathSeparator))
	for i := len(segs); i >= 1; i-- {
		candidate := filepath.Join(wsAbs, filepath.Join(segs[:i]...))
		if !dirExists(candidate) {
			continue
		}
		if i > 1 && !hasMeta(candidate) {
			continue
		}
		return s.Get(ctx, strings.Join(segs[:i], "/"))
	}
	return nil, fmt.Errorf("%w: not inside a workspace (%s)", ErrNotFound, abs)
}

// ProjectAt returns the nearest project containing dir, or nil when dir is
// not inside any project.
//
// Used to infer the parent for `arat new`. Walking past a task workspace to
// its containing project is deliberate: task workspaces cannot hold children,
// so creating a workspace while standing in one means "a sibling in the same
// project", not "a child of this task".
func (s *Service) ProjectAt(ctx context.Context, dir string) (*Workspace, error) {
	ws, err := s.WorkspaceAt(ctx, dir)
	if err != nil {
		return nil, err
	}
	if ws.IsProject() {
		return ws, nil
	}
	if ws.Parent == "" {
		return nil, nil
	}
	return s.Get(ctx, ws.Parent)
}
