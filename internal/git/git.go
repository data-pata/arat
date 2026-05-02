// Package git is a thin wrapper around the `git` CLI scoped to a working dir.
package git

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Runner runs an external command and returns combined stdout+stderr.
type Runner func(ctx context.Context, dir, name string, args ...string) (stdout []byte, stderr []byte, err error)

// Git is the wrapper. Use New() for the real implementation.
type Git struct{ run Runner }

// New returns a Git that shells out to the real `git` binary.
func New() *Git { return &Git{run: execRunner} }

// NewWithRunner returns a Git using the given Runner. Used by tests.
func NewWithRunner(r Runner) *Git { return &Git{run: r} }

func execRunner(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// Inspection is the snapshot captured for a single worktree path.
type Inspection struct {
	Branch   string // empty if detached HEAD
	Dirty    bool   // working tree has uncommitted changes
	Unpushed bool   // commits ahead of upstream (or no upstream and HEAD is non-empty)
	Stashes  int    // number of stash entries
}

// Inspect runs the cheap status checks needed for `arat ls`.
//
// Robustness: if a sub-step fails (e.g. no upstream), we surface a sensible
// default rather than aborting the whole inspection. The caller only needs
// best-effort signal.
func (g *Git) Inspect(ctx context.Context, dir string) (Inspection, error) {
	var ins Inspection

	out, _, err := g.run(ctx, dir, "git", "rev-parse", "--is-inside-work-tree")
	if err != nil {
		return ins, fmt.Errorf("not a git worktree: %s", dir)
	}
	if strings.TrimSpace(string(out)) != "true" {
		return ins, fmt.Errorf("not a git worktree: %s", dir)
	}

	if out, _, err := g.run(ctx, dir, "git", "branch", "--show-current"); err == nil {
		ins.Branch = strings.TrimSpace(string(out))
	}

	if out, _, err := g.run(ctx, dir, "git", "status", "--porcelain"); err == nil {
		ins.Dirty = len(bytes.TrimSpace(out)) > 0
	}

	// Unpushed: prefer @{upstream}..HEAD; if no upstream is set, fall back to
	// "any commits at all" being effectively unpushed (rare for fresh worktrees,
	// noisy otherwise — so we only flag if upstream check explicitly says yes).
	if out, _, err := g.run(ctx, dir, "git", "log", "@{upstream}..HEAD", "--oneline"); err == nil {
		ins.Unpushed = len(bytes.TrimSpace(out)) > 0
	}

	if out, _, err := g.run(ctx, dir, "git", "stash", "list"); err == nil {
		s := strings.TrimSpace(string(out))
		if s != "" {
			ins.Stashes = strings.Count(s, "\n") + 1
		}
	}

	return ins, nil
}

// IsWorktree returns true if dir is the root of a git worktree (i.e. dir
// itself contains a `.git` file or directory). Subdirectories of a worktree
// are NOT considered worktree roots even though `git rev-parse` would say
// they're inside a work tree — we want a precise answer here.
func (g *Git) IsWorktree(ctx context.Context, dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return false
	}
	out, _, err := g.run(ctx, dir, "git", "rev-parse", "--is-inside-work-tree")
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

// CanonicalRepoName returns the basename of the canonical repository for the
// worktree rooted at dir. For example, a worktree at
// /home/x/git/org/feat/foo/core-mono linked to a canonical clone at
// /home/x/git/org/core-mono returns "core-mono".
//
// Returns "" if it can't be determined.
func (g *Git) CanonicalRepoName(ctx context.Context, dir string) string {
	p := g.CanonicalRepoPath(ctx, dir)
	if p == "" {
		return ""
	}
	return filepath.Base(p)
}

// CanonicalRepoPath returns the absolute path of the canonical repository for
// the worktree rooted at dir (i.e. the parent of `git rev-parse --git-common-dir`).
// Returns "" if it can't be determined.
func (g *Git) CanonicalRepoPath(ctx context.Context, dir string) string {
	out, _, err := g.run(ctx, dir, "git", "rev-parse", "--git-common-dir")
	if err != nil {
		return ""
	}
	gitDir := strings.TrimSpace(string(out))
	if gitDir == "" {
		return ""
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(dir, gitDir)
	}
	return filepath.Dir(gitDir)
}

// Fetch runs `git fetch origin` in the canonical repo at dir.
func (g *Git) Fetch(ctx context.Context, dir string) error {
	_, errOut, err := g.run(ctx, dir, "git", "fetch", "origin")
	if err != nil {
		return fmt.Errorf("git fetch origin (in %s): %w: %s", dir, err, strings.TrimSpace(string(errOut)))
	}
	return nil
}

// WorktreeAdd runs `git worktree add -b <branch> <target> <base>` in the canonical
// repo at repoDir. Returns an error if base does not resolve.
func (g *Git) WorktreeAdd(ctx context.Context, repoDir, branch, target, base string) error {
	_, errOut, err := g.run(ctx, repoDir, "git", "worktree", "add", "-b", branch, target, base)
	if err != nil {
		return fmt.Errorf("git worktree add (in %s, base %s): %w: %s", repoDir, base, err, strings.TrimSpace(string(errOut)))
	}
	return nil
}

// WorktreeRemove runs `git worktree remove [--force] <target>` in repoDir.
func (g *Git) WorktreeRemove(ctx context.Context, repoDir, target string, force bool) error {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, target)
	_, errOut, err := g.run(ctx, repoDir, "git", args...)
	if err != nil {
		return fmt.Errorf("git worktree remove (in %s): %w: %s", repoDir, err, strings.TrimSpace(string(errOut)))
	}
	return nil
}

// BranchRename runs `git branch -m <from> <to>` in repoDir. Tolerates "from"
// being already named "to" (returns nil).
func (g *Git) BranchRename(ctx context.Context, repoDir, from, to string) error {
	if from == to {
		return nil
	}
	_, errOut, err := g.run(ctx, repoDir, "git", "branch", "-m", from, to)
	if err != nil {
		return fmt.Errorf("git branch -m %s %s (in %s): %w: %s", from, to, repoDir, err, strings.TrimSpace(string(errOut)))
	}
	return nil
}

// WorktreeRepair runs `git worktree repair` in repoDir to fix .git pointers
// after a worktree directory has been moved on disk.
func (g *Git) WorktreeRepair(ctx context.Context, repoDir string) error {
	_, errOut, err := g.run(ctx, repoDir, "git", "worktree", "repair")
	if err != nil {
		return fmt.Errorf("git worktree repair (in %s): %w: %s", repoDir, err, strings.TrimSpace(string(errOut)))
	}
	return nil
}

// BranchDelete runs `git branch -d|-D <branch>` in repoDir.
func (g *Git) BranchDelete(ctx context.Context, repoDir, branch string, force bool) error {
	flag := "-d"
	if force {
		flag = "-D"
	}
	_, errOut, err := g.run(ctx, repoDir, "git", "branch", flag, branch)
	if err != nil {
		return fmt.Errorf("git branch %s %s (in %s): %w: %s", flag, branch, repoDir, err, strings.TrimSpace(string(errOut)))
	}
	return nil
}
