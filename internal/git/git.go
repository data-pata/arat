// Package git is a thin wrapper around the `git` CLI scoped to a working dir.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ErrCmd is matched (errors.Is) by every error a failed git subprocess
// returns from this package. Callers use it to classify a failure as "the
// external tool failed" — arat's exit 6 — without string matching; an error
// not carrying it is arat's own.
var ErrCmd = errors.New("git command failed")

// cmdError carries the rendered message while matching both ErrCmd and the
// underlying exec error via the multi-error Unwrap.
type cmdError struct {
	msg string
	err error
}

func (e *cmdError) Error() string { return e.msg }
func (e *cmdError) Unwrap() []error {
	if e.err == nil {
		return []error{ErrCmd}
	}
	return []error{ErrCmd, e.err}
}

// cmdErrorf wraps a failed subprocess's error with a printf-rendered message.
func cmdErrorf(err error, format string, args ...any) error {
	return &cmdError{msg: fmt.Sprintf(format, args...), err: err}
}

// Runner runs an external command and returns combined stdout+stderr.
type Runner func(ctx context.Context, dir, name string, args ...string) (stdout []byte, stderr []byte, err error)

// Git is the wrapper. Use New() for the real implementation.
type Git struct{ run Runner }

// New returns a Git that shells out to the real `git` binary.
func New() *Git { return &Git{run: execRunner} }

// NewWithTimeout is New with a deadline on every subprocess. Zero d means no
// deadline. The bound is per git invocation, not per arat command: a
// multi-repo `arat new` runs many fetches, each entitled to the full budget.
func NewWithTimeout(d time.Duration) *Git { return &Git{run: timeoutRunner(execRunner, d)} }

// NewTraced is NewWithTimeout with a one-line trace per subprocess written
// to w: argv, directory, duration, and the failure if any. Everything arat
// does is a git subprocess, so this is the switch for "which commands ran
// and what did each cost" (wired from the ARAT_TRACE env var).
func NewTraced(d time.Duration, w io.Writer) *Git {
	return &Git{run: traceRunner(timeoutRunner(execRunner, d), w)}
}

// NewWithRunner returns a Git using the given Runner. Used by tests.
func NewWithRunner(r Runner) *Git { return &Git{run: r} }

// timeoutRunner bounds each invocation of r with its own deadline.
func timeoutRunner(r Runner, d time.Duration) Runner {
	if d <= 0 {
		return r
	}
	return func(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, error) {
		ctx, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		return r(ctx, dir, name, args...)
	}
}

// traceRunner logs each invocation of r to w after it completes. Wrapped
// outside the timeout so the logged duration includes a deadline kill.
func traceRunner(r Runner, w io.Writer) Runner {
	return func(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, error) {
		start := time.Now()
		stdout, stderr, err := r(ctx, dir, name, args...)
		status := "ok"
		if err != nil {
			status = err.Error()
		}
		fmt.Fprintf(w, "trace: %s %s (in %s) %s [%s]\n",
			name, strings.Join(args, " "), dir, time.Since(start).Round(time.Millisecond), status)
		return stdout, stderr, err
	}
}

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
	Unpushed bool   // commits not on the upstream (or, lacking one, on no remote branch at all)
	Stashes  int    // stash entries made on this worktree's branch
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

	// Unpushed: prefer @{upstream}..HEAD. Without an upstream — the normal
	// state for a branch created off a local base, e.g. `arat new
	// --from-parent` — fall back to "commits on no remote branch at all".
	// Skipping the fallback would hide exactly the commits that exist
	// nowhere but this clone, which is what the unpushed signal (and the
	// `arat rm` guard built on it) is for. A repo with no remotes is exempt:
	// there is nowhere to push to, so flagging every commit forever would
	// only train the user to reach for --force.
	if out, _, err := g.run(ctx, dir, "git", "log", "@{upstream}..HEAD", "--oneline"); err == nil {
		ins.Unpushed = len(bytes.TrimSpace(out)) > 0
	} else if remotes, _, err := g.run(ctx, dir, "git", "remote"); err == nil && len(bytes.TrimSpace(remotes)) > 0 {
		if out, _, err := g.run(ctx, dir, "git", "rev-list", "--count", "HEAD", "--not", "--remotes"); err == nil {
			ins.Unpushed = strings.TrimSpace(string(out)) != "0"
		}
	}

	// Stashes: refs/stash lives on the canonical clone, shared by every
	// worktree, so a raw count would light up every workspace of the repo
	// for one stash made in any of them. Attribute by branch instead: both
	// auto messages ("WIP on <branch>: …") and -m messages ("On <branch>:
	// …") name the branch the stash was made on.
	if out, _, err := g.run(ctx, dir, "git", "stash", "list"); err == nil {
		ins.Stashes = countStashesOnBranch(string(out), ins.Branch)
	}

	return ins, nil
}

// countStashesOnBranch counts stash-list lines recorded on the given branch.
// With no branch to attribute to (detached HEAD), every stash counts.
func countStashesOnBranch(stashList, branch string) int {
	lines := strings.Split(strings.TrimSpace(stashList), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return 0
	}
	if branch == "" {
		return len(lines)
	}
	needle := " on " + strings.ToLower(branch) + ":"
	n := 0
	for _, ln := range lines {
		if strings.Contains(strings.ToLower(ln), needle) {
			n++
		}
	}
	return n
}

// InspectFast returns what can be read about the worktree at dir from the
// filesystem alone, with no git subprocess: the checked-out branch ("" when
// detached or unreadable) and the canonical repo path ("" when dir is the
// canonical clone itself or the layout is unrecognised).
//
// It exists for listings: one `git status` per worktree makes `arat ls`
// scale with worktree count times repo size, whereas two file reads make it
// effectively free. Anything beyond branch and origin — dirtiness, unpushed
// commits, stashes — genuinely needs git and stays in Inspect.
func (g *Git) InspectFast(dir string) (branch, canonical string) {
	gitPath := filepath.Join(dir, ".git")
	fi, err := os.Stat(gitPath)
	if err != nil {
		return "", ""
	}

	headPath := filepath.Join(gitPath, "HEAD")
	if !fi.IsDir() {
		// A linked worktree: .git is a file "gitdir: <canonical>/.git/worktrees/<id>".
		data, err := os.ReadFile(gitPath)
		if err != nil {
			return "", ""
		}
		line, _, _ := strings.Cut(strings.TrimSpace(string(data)), "\n")
		gitdir, ok := strings.CutPrefix(line, "gitdir: ")
		if !ok {
			return "", ""
		}
		if !filepath.IsAbs(gitdir) {
			gitdir = filepath.Join(dir, gitdir)
		}
		headPath = filepath.Join(gitdir, "HEAD")
		if i := strings.LastIndex(filepath.ToSlash(gitdir), "/.git/worktrees/"); i >= 0 {
			canonical = filepath.FromSlash(filepath.ToSlash(gitdir)[:i])
		}
	}

	data, err := os.ReadFile(headPath)
	if err != nil {
		return "", canonical
	}
	head, _, _ := strings.Cut(strings.TrimSpace(string(data)), "\n")
	if b, ok := strings.CutPrefix(head, "ref: refs/heads/"); ok {
		branch = b
	}
	return branch, canonical
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
		return cmdErrorf(err, "git fetch origin (in %s): %s: %v", dir, strings.TrimSpace(string(errOut)), err)
	}
	return nil
}

// WorktreeAdd runs `git worktree add -b <branch> <target> <base>` in the canonical
// repo at repoDir. Returns an error if base does not resolve.
func (g *Git) WorktreeAdd(ctx context.Context, repoDir, branch, target, base string) error {
	_, errOut, err := g.run(ctx, repoDir, "git", "worktree", "add", "-b", branch, target, base)
	if err != nil {
		return cmdErrorf(err, "git worktree add (in %s, base %s): %s: %v", repoDir, base, strings.TrimSpace(string(errOut)), err)
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
		return cmdErrorf(err, "git worktree remove (in %s): %s: %v", repoDir, strings.TrimSpace(string(errOut)), err)
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
		return cmdErrorf(err, "git branch -m %s %s (in %s): %s: %v", from, to, repoDir, strings.TrimSpace(string(errOut)), err)
	}
	return nil
}

// WorktreeRepair runs `git worktree repair [<path>...]` in repoDir to fix
// worktree registrations after worktree directories have been moved on disk.
//
// The moved worktrees' new paths must be passed: per git's semantics, repair
// run from the main worktree without arguments only fixes the linked-to-main
// direction (worktrees whose .git file points at a moved main repo). To fix
// the main-to-linked direction — the one a workspace rename breaks — git has
// to be told where the worktrees are now.
func (g *Git) WorktreeRepair(ctx context.Context, repoDir string, worktreePaths ...string) error {
	args := append([]string{"worktree", "repair"}, worktreePaths...)
	_, errOut, err := g.run(ctx, repoDir, "git", args...)
	if err != nil {
		return cmdErrorf(err, "git worktree repair (in %s): %s: %v", repoDir, strings.TrimSpace(string(errOut)), err)
	}
	return nil
}

// WorktreePrune runs `git worktree prune` in repoDir, clearing registrations
// whose directories no longer exist. Used by failure cleanup: a worktree add
// killed mid-flight (cancellation, crash) can leave a registration that
// `git worktree remove` no longer matches, and prune is the only git verb
// that clears those.
func (g *Git) WorktreePrune(ctx context.Context, repoDir string) error {
	_, errOut, err := g.run(ctx, repoDir, "git", "worktree", "prune")
	if err != nil {
		return cmdErrorf(err, "git worktree prune (in %s): %s: %v", repoDir, strings.TrimSpace(string(errOut)), err)
	}
	return nil
}

// BranchExists reports whether refs/heads/<branch> exists in repoDir.
func (g *Git) BranchExists(ctx context.Context, repoDir, branch string) bool {
	_, _, err := g.run(ctx, repoDir, "git", "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// BranchDelete runs `git branch -d|-D <branch>` in repoDir.
func (g *Git) BranchDelete(ctx context.Context, repoDir, branch string, force bool) error {
	flag := "-d"
	if force {
		flag = "-D"
	}
	_, errOut, err := g.run(ctx, repoDir, "git", "branch", flag, branch)
	if err != nil {
		return cmdErrorf(err, "git branch %s %s (in %s): %s: %v", flag, branch, repoDir, strings.TrimSpace(string(errOut)), err)
	}
	return nil
}
