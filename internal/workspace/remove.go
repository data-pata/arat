package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// RemoveOptions controls Service.Remove.
type RemoveOptions struct {
	Name         string
	Force        bool // skip safety checks (dirty/unpushed/stashes)
	KeepBranches bool // do not delete the branches when removing worktrees
}

// Remove deletes a workspace and its worktrees. By default it refuses if any
// worktree is dirty, has unpushed commits, or has stashes — pass Force to
// override.
func (s *Service) Remove(ctx context.Context, opts RemoveOptions) error {
	full := filepath.Join(s.WorkspacesDir, opts.Name)
	info, err := os.Stat(full)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: %s", ErrNotFound, opts.Name)
		}
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %s is not a directory", ErrNotFound, opts.Name)
	}

	worktrees, err := s.locateWorktrees(ctx, full)
	if err != nil {
		return err
	}

	if !opts.Force {
		if reasons := s.collectPreconditions(ctx, worktrees); len(reasons) > 0 {
			return &ErrPrecondition{Reasons: reasons}
		}
	}

	for _, wt := range worktrees {
		canonical := s.Git.CanonicalRepoPath(ctx, wt.path)
		if canonical == "" {
			return fmt.Errorf("could not resolve canonical repo for %s", wt.path)
		}
		if err := s.Git.WorktreeRemove(ctx, canonical, wt.path, opts.Force); err != nil {
			return err
		}
		if !opts.KeepBranches && wt.branch != "" {
			// best-effort branch delete; if checked out elsewhere it'll fail and we warn.
			if err := s.Git.BranchDelete(ctx, canonical, wt.branch, true); err != nil {
				return fmt.Errorf("worktree removed but branch delete failed (%s in %s): %w", wt.branch, canonical, err)
			}
		}
	}

	return os.RemoveAll(full)
}

type worktreeRef struct {
	path   string
	branch string
}

// locateWorktrees finds the worktrees inside a workspace dir.
//
// If the workspace dir itself is a worktree (single-repo workspace), returns
// just that. Otherwise, iterates immediate subdirs.
func (s *Service) locateWorktrees(ctx context.Context, workspaceDir string) ([]worktreeRef, error) {
	if s.Git.IsWorktree(ctx, workspaceDir) {
		ins, _ := s.Git.Inspect(ctx, workspaceDir)
		return []worktreeRef{{path: workspaceDir, branch: ins.Branch}}, nil
	}

	entries, err := os.ReadDir(workspaceDir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", workspaceDir, err)
	}
	var out []worktreeRef
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(workspaceDir, e.Name())
		if !s.Git.IsWorktree(ctx, p) {
			continue
		}
		ins, _ := s.Git.Inspect(ctx, p)
		out = append(out, worktreeRef{path: p, branch: ins.Branch})
	}
	return out, nil
}

func (s *Service) collectPreconditions(ctx context.Context, worktrees []worktreeRef) []string {
	var reasons []string
	for _, wt := range worktrees {
		ins, err := s.Git.Inspect(ctx, wt.path)
		if err != nil {
			reasons = append(reasons, fmt.Sprintf("%s: inspect failed: %v", wt.path, err))
			continue
		}
		if ins.Dirty {
			reasons = append(reasons, fmt.Sprintf("%s: uncommitted changes", wt.path))
		}
		if ins.Unpushed {
			reasons = append(reasons, fmt.Sprintf("%s: unpushed commits on %s", wt.path, ins.Branch))
		}
		if ins.Stashes > 0 {
			reasons = append(reasons, fmt.Sprintf("%s: %d stash entries", wt.path, ins.Stashes))
		}
	}
	return reasons
}
