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
	Force        bool // skip safety checks (dirty/unpushed/scratch content)
	KeepBranches bool // do not delete the branches when removing worktrees
	// Recursive permits removing a workspace that still contains nested
	// workspaces. Without it, such a removal is refused: deleting the
	// directory takes every workspace under it with it, and that is too
	// much to do on the strength of one name on the command line.
	Recursive bool
	// DeleteScratch permits deleting a non-empty claude_workspace/ scratch
	// dir. Scratch content is never committed or pushed anywhere, so without
	// this (or Force) Remove refuses with *ErrScratchNotEmpty; the caller is
	// expected to show the listing, ask, and re-run with this set.
	DeleteScratch bool
}

// ScratchContent is the claude_workspace/ content of one workspace slated
// for removal, reported on *ErrScratchNotEmpty.
type ScratchContent struct {
	Ref   string
	Files []string // slash-separated paths relative to the scratch dir, sorted
}

// ErrScratchNotEmpty means Remove was asked to delete a workspace whose
// claude_workspace/ still holds content, without Force or DeleteScratch.
//
// It is kept apart from ErrPrecondition for the same reason ErrNotEmpty is:
// the git preconditions guard work that survives elsewhere (commits, stashes
// on the canonical clone), whereas scratch content exists nowhere else at
// all, so callers surface it with its full listing before letting it go.
type ErrScratchNotEmpty struct {
	Contents []ScratchContent
}

func (e *ErrScratchNotEmpty) Error() string {
	refs := make([]string, 0, len(e.Contents))
	files := 0
	for _, c := range e.Contents {
		refs = append(refs, c.Ref)
		files += len(c.Files)
	}
	return fmt.Sprintf("claude_workspace in %s holds %d %s that would be deleted with no way back",
		strings.Join(refs, ", "), files, pluralWord(files, "file", "files"))
}

// pluralWord picks the singular or plural form for a count.
func pluralWord(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// RemoveResult is the outcome of Service.Remove. On error it is returned
// alongside the error, partially populated, so callers can report how far
// the teardown got instead of only that one git command failed — the same
// contract AddRepos keeps, and more important here because the operation is
// destructive.
type RemoveResult struct {
	// Removed lists the refs of every workspace deleted: the target first,
	// then its descendants. Recursive removal is the one place the tool
	// destroys things the user did not name, so the caller can show exactly
	// what went. Populated only once the removal actually completed — on a
	// partial failure it stays empty and RemovedWorktrees carries the
	// progress instead.
	Removed []string
	// RemovedWorktrees lists the worktree paths torn down so far, appended
	// as each removal completes. On success it covers every worktree; on
	// error it is the teardown's actual progress.
	RemovedWorktrees []string
	// Warnings lists non-fatal teardown problems, currently branch deletes
	// that failed (e.g. the branch checked out in a worktree outside this
	// workspace). The worktree is already gone when the delete runs, so
	// aborting the removal over it would leave a half-torn-down workspace
	// over state one git command can clear.
	Warnings []string
	// StashedRepos lists canonical repos whose stash entries were touched by
	// the removal, one entry per canonical repo. The stash refs themselves
	// live on the canonical clone's .git/refs/stash and survive the worktree
	// removal — callers can surface a hint pointing the user there so the
	// stashes don't get forgotten.
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
	// worktrees and never double-counts a child's. The path-dedupe below is
	// defence in depth for inputs that break that assumption anyway (e.g. a
	// repo whose committed tree carries a marker file) — removing the same
	// worktree twice would fail halfway through the teardown.
	var worktrees []worktree
	seen := map[string]struct{}{}
	targets := append([]Workspace{*ws}, nested...)
	for _, target := range targets {
		found, err := s.locateWorktrees(ctx, target.Path)
		if err != nil {
			return nil, err
		}
		for _, wt := range found {
			if _, dup := seen[wt.path]; dup {
				continue
			}
			seen[wt.path] = struct{}{}
			worktrees = append(worktrees, wt)
		}
	}

	if !opts.Force {
		if reasons := blockingReasons(worktrees); len(reasons) > 0 {
			return nil, &ErrPrecondition{Reasons: reasons}
		}
	}

	// Scratch content is checked after the git preconditions so an
	// interactive caller confirms its deletion only once the git state is
	// already clear — otherwise a confirmed re-run could still refuse.
	if !opts.Force && !opts.DeleteScratch {
		var contents []ScratchContent
		for _, target := range targets {
			files, err := scratchFiles(target.Path)
			if err != nil {
				return nil, err
			}
			if len(files) > 0 {
				contents = append(contents, ScratchContent{Ref: target.Ref, Files: files})
			}
		}
		if len(contents) > 0 {
			return nil, &ErrScratchNotEmpty{Contents: contents}
		}
	}

	res := &RemoveResult{}
	// One stash note per canonical repo: the stash refs live there, so
	// repeating the note for every removed worktree of the same repo would
	// just re-announce the same refs.
	stashedIdx := map[string]int{}
	for _, wt := range worktrees {
		canonical := s.Git.CanonicalRepoPath(ctx, wt.path)
		if canonical == "" {
			return res, fmt.Errorf("could not resolve canonical repo for %s", wt.path)
		}
		if wt.ins != nil && wt.ins.Stashes > 0 {
			if i, dup := stashedIdx[canonical]; dup {
				res.StashedRepos[i].Stashes += wt.ins.Stashes
			} else {
				stashedIdx[canonical] = len(res.StashedRepos)
				res.StashedRepos = append(res.StashedRepos, StashedRepo{
					Path:          wt.path,
					CanonicalRepo: canonical,
					Stashes:       wt.ins.Stashes,
				})
			}
		}
		if err := s.Git.WorktreeRemove(ctx, canonical, wt.path, opts.Force); err != nil {
			// Partial teardown: hand back what already went so the caller
			// can say how far it got. Re-running Remove is safe — worktrees
			// already gone are simply no longer found.
			return res, fmt.Errorf("removed %d of %d worktrees, then: %w", len(res.RemovedWorktrees), len(worktrees), err)
		}
		res.RemovedWorktrees = append(res.RemovedWorktrees, wt.path)
		if !opts.KeepBranches && wt.branch() != "" {
			// Best-effort: the worktree is gone by now, so aborting here
			// would strand a half-removed workspace over a branch the user
			// can delete with one command. Warn and carry on.
			if err := s.Git.BranchDelete(ctx, canonical, wt.branch(), true); err != nil {
				res.Warnings = append(res.Warnings,
					fmt.Sprintf("branch %s in %s not deleted: %v", wt.branch(), canonical, err))
			}
		}
	}

	if err := os.RemoveAll(full); err != nil {
		return res, err
	}
	for _, target := range targets {
		res.Removed = append(res.Removed, target.Ref)
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
