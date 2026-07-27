package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// AddReposOptions controls Service.AddRepos.
type AddReposOptions struct {
	Workspace string   // existing workspace name or ref
	Repos     []string // repos to add (must exist as clones at Root)
	Base      string   // optional branch base; defaults to s.Base or "origin/HEAD"

	// Recursive also adds the repos to every workspace nested under
	// Workspace. In this mode a repo a workspace already carries is skipped
	// rather than an error — fanning a repo out over a tree where some
	// members already have it is the point of the flag — and a workspace
	// with a single-repo layout is skipped whole, with the reason reported
	// on its outcome.
	Recursive bool
}

// AddReposResult is what AddRepos returns: the refreshed target workspace and
// per-workspace outcomes, target first. Without Recursive there is exactly
// one outcome and its Skipped is always empty (a repo that is already present
// is an error instead).
type AddReposResult struct {
	Workspace *Workspace
	Outcomes  []WorkspaceAdd
}

// WorkspaceAdd is the outcome of adding repos to one workspace.
type WorkspaceAdd struct {
	Ref     string
	Added   []RepoStatus
	Skipped []string // reasons, e.g. "ui-app: already present" or "single-repo layout"
}

// AddRepos adds one or more git worktrees to an existing multi-repo
// workspace, and with Recursive to every workspace nested under it.
//
// The new worktrees use each workspace's existing feature branch (derived
// from its first existing worktree, falling back to BranchName from prefix/
// short/ticket). Each new branch is created in its own canonical repo,
// branched off the configured base. Nesting deliberately plays no part in the
// base: fanning a repo out over a tree starts every workspace from the same
// base, exactly like creating them without --from-parent would.
//
// Errors:
//   - ErrNotFound: the workspace doesn't exist, or a requested canonical clone
//     is missing at Root.
//   - ErrAlreadyExists (without Recursive): a subdir for one of the requested
//     repos already exists in the workspace.
//   - ErrPrecondition (without Recursive): the workspace is a single-repo
//     layout (the workspace dir itself is a worktree), so adding sibling
//     repos isn't possible.
func (s *Service) AddRepos(ctx context.Context, opts AddReposOptions) (*AddReposResult, error) {
	if len(opts.Repos) == 0 {
		return nil, errors.New("at least one repo is required")
	}

	ws, err := s.Get(ctx, opts.Workspace)
	if err != nil {
		return nil, err
	}

	// Canonical clones must exist regardless of mode; a missing one fails
	// the whole call before any worktree is created anywhere.
	for _, repo := range opts.Repos {
		canonical := filepath.Join(s.Root, repo)
		if !dirExists(canonical) {
			return nil, fmt.Errorf("%w: canonical repo %s not found at %s", ErrNotFound, repo, canonical)
		}
	}

	base := opts.Base
	if base == "" {
		base = s.Base
	}
	if base == "" {
		base = "origin/HEAD"
	}

	targets := []Workspace{*ws}
	if opts.Recursive {
		targets = append(targets, Descendants(*ws)...)
	}

	res := &AddReposResult{}
	for _, target := range targets {
		outcome, err := s.addReposToOne(ctx, target, opts.Repos, base, opts.Recursive)
		res.Outcomes = append(res.Outcomes, outcome)
		if err != nil {
			// Return what already happened along with the error: in a
			// fan-out, workspaces before this one really did get the repo,
			// and hiding that would leave the user unaware half the tree
			// changed. Re-running after the fix is safe (lenient skips).
			return res, err
		}
	}

	updated, err := s.Get(ctx, ws.Ref)
	if err != nil {
		return res, err
	}
	res.Workspace = updated
	return res, nil
}

// addReposToOne adds the requested repos to a single workspace. With lenient
// set (recursive mode), conditions that would be errors for an explicitly
// named workspace — already present, single-repo layout — become skip reasons
// instead, so one already-covered child doesn't abort the fan-out.
func (s *Service) addReposToOne(ctx context.Context, ws Workspace, repos []string, base string, lenient bool) (WorkspaceAdd, error) {
	outcome := WorkspaceAdd{Ref: ws.Ref}

	if s.Git.IsWorktree(ctx, ws.Path) {
		if lenient {
			outcome.Skipped = append(outcome.Skipped, "single-repo layout")
			return outcome, nil
		}
		return outcome, &ErrPrecondition{Reasons: []string{
			fmt.Sprintf("workspace %s is a single-repo layout (workspace dir is itself a worktree); adding sibling repos requires a multi-repo layout", ws.Ref),
		}}
	}

	// Up-front validation so we don't half-add when one entry is bad.
	existing := make(map[string]struct{}, len(ws.Repos))
	for _, r := range ws.Repos {
		existing[r.Name] = struct{}{}
	}
	branch := deriveBranch(&ws, s.BranchPrefix)

	var toAdd []string
	for _, repo := range repos {
		target := filepath.Join(ws.Path, repo)
		// Say what actually occupies the name: a repo the workspace carries
		// reads differently from a child workspace or stray directory that
		// happens to share it, and only the first is "already present".
		blocked := ""
		if _, ok := existing[repo]; ok {
			blocked = repo + ": already present"
		} else if _, err := os.Stat(target); err == nil {
			if hasMeta(target) {
				blocked = repo + ": blocked by the child workspace of the same name"
			} else {
				blocked = repo + ": a directory with that name already exists"
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return outcome, fmt.Errorf("stat %s: %w", target, err)
		} else if s.Git.BranchExists(ctx, filepath.Join(s.Root, repo), branch) {
			// Surface the collision as an arat-level message rather than
			// letting `git worktree add` fail with a raw fatal mid-run.
			blocked = fmt.Sprintf("%s: branch %s already exists there — another workspace's, or kept by rm --keep-branches", repo, branch)
		}
		if blocked != "" {
			if lenient {
				outcome.Skipped = append(outcome.Skipped, blocked)
				continue
			}
			return outcome, fmt.Errorf("%w: %s (workspace %s)", ErrAlreadyExists, blocked, ws.Ref)
		}
		toAdd = append(toAdd, repo)
	}
	for _, repo := range toAdd {
		canonical := filepath.Join(s.Root, repo)
		fetchErr := s.Git.Fetch(ctx, canonical)
		target := filepath.Join(ws.Path, repo)
		if err := s.Git.WorktreeAdd(ctx, canonical, branch, target, base); err != nil {
			if fetchErr != nil {
				return outcome, fmt.Errorf("worktree add for %s in %s: %w (preceding fetch error: %v)", repo, ws.Ref, err, fetchErr)
			}
			return outcome, fmt.Errorf("worktree add for %s in %s: %w", repo, ws.Ref, err)
		}
		outcome.Added = append(outcome.Added, RepoStatus{Name: repo, Path: target, Branch: branch})
	}

	// Regenerate <name>.code-workspace iff one already exists. Don't create
	// one here — that's a config / `arat new` decision.
	if len(outcome.Added) > 0 {
		cwPath := filepath.Join(ws.Path, ws.Name+".code-workspace")
		if _, err := os.Stat(cwPath); err == nil {
			names := make([]string, 0, len(ws.Repos)+len(outcome.Added))
			for _, r := range ws.Repos {
				names = append(names, r.Name)
			}
			for _, r := range outcome.Added {
				names = append(names, r.Name)
			}
			if err := writeCodeWorkspace(ws.Path, ws.Name, names); err != nil {
				return outcome, fmt.Errorf("regenerate code-workspace: %w", err)
			}
		}
	}
	return outcome, nil
}

// deriveBranch returns the feature branch shared by all worktrees in the
// workspace. Prefer reading from an existing worktree (handles renames done
// by `ticket attach`); fall back to recomputing from the configured prefix.
func deriveBranch(ws *Workspace, prefix string) string {
	for _, r := range ws.Repos {
		if r.Branch != "" {
			return r.Branch
		}
	}
	return BranchName(prefix, ws.ShortName, ws.Ticket)
}
