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
		if err != nil {
			return nil, err
		}
		res.Outcomes = append(res.Outcomes, outcome)
	}

	updated, err := s.Get(ctx, ws.Ref)
	if err != nil {
		return nil, err
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
	var toAdd []string
	for _, repo := range repos {
		present := false
		if _, ok := existing[repo]; ok {
			present = true
		} else if _, err := os.Stat(filepath.Join(ws.Path, repo)); err == nil {
			present = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return outcome, fmt.Errorf("stat %s: %w", filepath.Join(ws.Path, repo), err)
		}
		if present {
			if lenient {
				outcome.Skipped = append(outcome.Skipped, repo+": already present")
				continue
			}
			return outcome, fmt.Errorf("%w: %s already in workspace %s", ErrAlreadyExists, repo, ws.Ref)
		}
		toAdd = append(toAdd, repo)
	}

	branch := deriveBranch(&ws, s.BranchPrefix)
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
