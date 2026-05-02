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
	Workspace string   // existing workspace name
	Repos     []string // repos to add (must exist as clones at Root)
	Base      string   // optional branch base; defaults to s.Base or "origin/HEAD"
}

// AddReposResult is what AddRepos returns: the refreshed workspace and the
// repos that were added in this call.
type AddReposResult struct {
	Workspace *Workspace
	Added     []RepoStatus
}

// AddRepos adds one or more git worktrees to an existing multi-repo workspace.
//
// The new worktrees use the workspace's existing feature branch (derived from
// the first existing worktree, falling back to BranchName from prefix/short/
// ticket). Each new branch is created in its own canonical repo, branched off
// the configured base.
//
// Errors:
//   - ErrNotFound: the workspace doesn't exist, or a requested canonical clone
//     is missing at Root.
//   - ErrAlreadyExists: a subdir for one of the requested repos already exists
//     in the workspace.
//   - ErrPrecondition: the workspace is a single-repo layout (the workspace
//     dir itself is a worktree), so adding sibling repos isn't possible.
func (s *Service) AddRepos(ctx context.Context, opts AddReposOptions) (*AddReposResult, error) {
	if len(opts.Repos) == 0 {
		return nil, errors.New("at least one repo is required")
	}

	ws, err := s.Get(ctx, opts.Workspace)
	if err != nil {
		return nil, err
	}

	if s.Git.IsWorktree(ctx, ws.Path) {
		return nil, &ErrPrecondition{Reasons: []string{
			fmt.Sprintf("workspace %s is a single-repo layout (workspace dir is itself a worktree); adding sibling repos requires a multi-repo layout", ws.Name),
		}}
	}

	branch := deriveBranch(ws, s.BranchPrefix)

	base := opts.Base
	if base == "" {
		base = s.Base
	}
	if base == "" {
		base = "origin/HEAD"
	}

	// Up-front validation so we don't half-add when one entry is bad.
	existing := make(map[string]struct{}, len(ws.Repos))
	for _, r := range ws.Repos {
		existing[r.Name] = struct{}{}
	}
	for _, repo := range opts.Repos {
		if _, ok := existing[repo]; ok {
			return nil, fmt.Errorf("%w: %s already in workspace %s", ErrAlreadyExists, repo, ws.Name)
		}
		canonical := filepath.Join(s.Root, repo)
		if !dirExists(canonical) {
			return nil, fmt.Errorf("%w: canonical repo %s not found at %s", ErrNotFound, repo, canonical)
		}
		target := filepath.Join(ws.Path, repo)
		if _, err := os.Stat(target); err == nil {
			return nil, fmt.Errorf("%w: %s already exists at %s", ErrAlreadyExists, repo, target)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("stat %s: %w", target, err)
		}
	}

	added := make([]RepoStatus, 0, len(opts.Repos))
	for _, repo := range opts.Repos {
		canonical := filepath.Join(s.Root, repo)
		fetchErr := s.Git.Fetch(ctx, canonical)
		target := filepath.Join(ws.Path, repo)
		if err := s.Git.WorktreeAdd(ctx, canonical, branch, target, base); err != nil {
			if fetchErr != nil {
				return nil, fmt.Errorf("worktree add for %s: %w (preceding fetch error: %v)", repo, err, fetchErr)
			}
			return nil, fmt.Errorf("worktree add for %s: %w", repo, err)
		}
		added = append(added, RepoStatus{Name: repo, Path: target, Branch: branch})
	}

	// Regenerate <name>.code-workspace iff one already exists. Don't create
	// one here — that's a config / `arat new` decision.
	cwPath := filepath.Join(ws.Path, ws.Name+".code-workspace")
	if _, err := os.Stat(cwPath); err == nil {
		repos := make([]string, 0, len(ws.Repos)+len(added))
		for _, r := range ws.Repos {
			repos = append(repos, r.Name)
		}
		for _, r := range added {
			repos = append(repos, r.Name)
		}
		if err := writeCodeWorkspace(ws.Path, ws.Name, repos); err != nil {
			return nil, fmt.Errorf("regenerate code-workspace: %w", err)
		}
	}

	updated, err := s.Get(ctx, ws.Name)
	if err != nil {
		return nil, err
	}
	return &AddReposResult{Workspace: updated, Added: added}, nil
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
