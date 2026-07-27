package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/data-pata/arat/internal/git"
)

// ErrNotEmpty means Remove was asked to delete a workspace that still
// contains nested workspaces, without RemoveOptions.Recursive.
//
// It is deliberately distinct from ErrPrecondition: the precondition errors
// are about losing *changes* and are cleared with --force, whereas this one
// is about losing whole *workspaces* and is cleared with --recursive. Telling
// the user to reach for --force here would be actively wrong.
type ErrNotEmpty struct {
	Ref      string
	Children []string
}

func (e *ErrNotEmpty) Error() string {
	noun := "workspaces"
	if len(e.Children) == 1 {
		noun = "workspace"
	}
	return fmt.Sprintf("%s contains %d nested %s: %s",
		e.Ref, len(e.Children), noun, strings.Join(e.Children, ", "))
}

// RemoveOptions controls Service.Remove.
type RemoveOptions struct {
	Name         string
	Force        bool // skip safety checks (dirty/unpushed)
	KeepBranches bool // do not delete the branches when removing worktrees
	// Recursive permits removing a workspace that still contains nested
	// workspaces. Without it, such a removal is refused: deleting the
	// directory takes every workspace under it with it, and that is too
	// much to do on the strength of one name on the command line.
	Recursive bool
}

// RemoveResult is the outcome of Service.Remove.
type RemoveResult struct {
	// StashedRepos lists worktrees that had stash entries at the time of
	// removal. The stash refs themselves live on the canonical clone's
	// .git/refs/stash and survive the worktree removal — callers can surface
	// a hint pointing the user there so the stashes don't get forgotten.
	StashedRepos []StashedRepo
}

// StashedRepo is one entry on RemoveResult.StashedRepos.
type StashedRepo struct {
	Path          string // the (now-removed) worktree path
	CanonicalRepo string // canonical clone where the stash ref still lives
	Stashes       int
}

// Remove deletes a workspace and its worktrees. By default it refuses if any
// worktree is dirty or has unpushed commits — pass Force to override. Stash
// entries do not block: the stash refs live on the canonical clone and
// survive worktree removal, so removal proceeds and the result lists the
// affected worktrees in StashedRepos.
func (s *Service) Remove(ctx context.Context, opts RemoveOptions) (*RemoveResult, error) {
	ws, err := s.Get(ctx, opts.Name)
	if err != nil {
		return nil, err
	}
	full := ws.Path

	// A workspace's directory contains its children, so removing it removes
	// them too. Require that to be stated explicitly. --force is about
	// losing *committed and uncommitted work*, this is about losing whole
	// workspaces, so it deliberately needs its own flag.
	nested := Descendants(*ws)
	if len(nested) > 0 && !opts.Recursive {
		refs := make([]string, 0, len(nested))
		for _, c := range nested {
			refs = append(refs, c.Ref)
		}
		return nil, &ErrNotEmpty{Ref: ws.Ref, Children: refs}
	}

	// Collect the worktrees of the workspace itself and of every workspace
	// below it. Each workspace dir is scanned on its own: a child workspace
	// directory is not a worktree, so scanning one picks up only its own
	// worktrees and never double-counts a child's.
	var worktrees []worktree
	for _, target := range append([]Workspace{*ws}, nested...) {
		found, err := s.locateWorktrees(ctx, target.Path)
		if err != nil {
			return nil, err
		}
		worktrees = append(worktrees, found...)
	}

	if !opts.Force {
		if reasons := blockingReasons(worktrees); len(reasons) > 0 {
			return nil, &ErrPrecondition{Reasons: reasons}
		}
	}

	res := &RemoveResult{}
	for _, wt := range worktrees {
		canonical := s.Git.CanonicalRepoPath(ctx, wt.path)
		if canonical == "" {
			return nil, fmt.Errorf("could not resolve canonical repo for %s", wt.path)
		}
		if wt.ins != nil && wt.ins.Stashes > 0 {
			res.StashedRepos = append(res.StashedRepos, StashedRepo{
				Path:          wt.path,
				CanonicalRepo: canonical,
				Stashes:       wt.ins.Stashes,
			})
		}
		if err := s.Git.WorktreeRemove(ctx, canonical, wt.path, opts.Force); err != nil {
			return nil, err
		}
		if !opts.KeepBranches && wt.branch() != "" {
			// best-effort branch delete; if checked out elsewhere it'll fail and we warn.
			if err := s.Git.BranchDelete(ctx, canonical, wt.branch(), true); err != nil {
				return nil, fmt.Errorf("worktree removed but branch delete failed (%s in %s): %w", wt.branch(), canonical, err)
			}
		}
	}

	if err := os.RemoveAll(full); err != nil {
		return nil, err
	}
	return res, nil
}

// worktree is one worktree found inside a workspace, with its inspection
// result captured in a single pass. ins is nil and err is set when Inspect
// failed; blockingReasons surfaces that as a precondition reason so callers
// don't silently proceed against an unknown state.
type worktree struct {
	path string
	ins  *git.Inspection
	err  error
}

// branch returns the worktree's branch, or "" if the inspection didn't yield
// one (detached HEAD or inspect error). Used by Remove to decide whether to
// attempt a branch delete after removing the worktree.
func (w worktree) branch() string {
	if w.ins == nil {
		return ""
	}
	return w.ins.Branch
}

// locateWorktrees finds the worktrees inside a workspace dir and inspects
// each one. If the workspace dir itself is a worktree (single-repo
// workspace), returns just that. Otherwise iterates immediate subdirs.
//
// Per-worktree Inspect errors are preserved on the worktree entry; the outer
// error is reserved for "couldn't read the workspace dir at all".
func (s *Service) locateWorktrees(ctx context.Context, workspaceDir string) ([]worktree, error) {
	if s.Git.IsWorktree(ctx, workspaceDir) {
		return []worktree{s.inspectWorktree(ctx, workspaceDir)}, nil
	}

	entries, err := os.ReadDir(workspaceDir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", workspaceDir, err)
	}
	var out []worktree
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(workspaceDir, e.Name())
		if !s.Git.IsWorktree(ctx, p) {
			continue
		}
		out = append(out, s.inspectWorktree(ctx, p))
	}
	return out, nil
}

func (s *Service) inspectWorktree(ctx context.Context, path string) worktree {
	ins, err := s.Git.Inspect(ctx, path)
	if err != nil {
		return worktree{path: path, err: err}
	}
	return worktree{path: path, ins: &ins}
}

// blockingReasons returns the reasons that make Remove refuse without
// --force. Stashes are intentionally not blocking: the stash refs live on
// the canonical clone and survive worktree removal, so the user won't lose
// them.
func blockingReasons(worktrees []worktree) []string {
	var reasons []string
	for _, wt := range worktrees {
		if wt.err != nil {
			reasons = append(reasons, fmt.Sprintf("%s: inspect failed: %v", wt.path, wt.err))
			continue
		}
		if wt.ins.Dirty {
			reasons = append(reasons, fmt.Sprintf("%s: uncommitted changes", wt.path))
		}
		if wt.ins.Unpushed {
			reasons = append(reasons, fmt.Sprintf("%s: unpushed commits on %s", wt.path, wt.ins.Branch))
		}
	}
	return reasons
}
