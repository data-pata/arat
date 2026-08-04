package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// newConcurrency caps the number of in-flight per-repo fetch+worktree-add jobs
// when materialising a workspace. Each job opens an SSH connection for the
// fetch, so the cap doubles as a politeness limit toward the remote.
const newConcurrency = 25

// NewOptions controls Service.New.
type NewOptions struct {
	ShortName string   // required, lowercase kebab-case
	Ticket    string   // optional; lowercase, must match TicketRE if non-empty
	Repos     []string // explicit repo list; if nil, falls back to default + auto_repos_glob

	// Kind selects a leaf task workspace (KindTask, the zero value) or a
	// project container (KindProject).
	//
	// The two differ in how Repos is defaulted: a task with no explicit
	// Repos falls back to default_repos + auto_repos_glob, whereas a
	// project with no explicit Repos gets no worktrees at all. Grouping is
	// a project's primary job, so materialising a full repo set for one is
	// opt-in rather than the default.
	Kind Kind

	// Parent is the ref of the workspace to create this one inside
	// ("q3-billing", "q3-billing/abc-12--invoice"). Empty creates at the top
	// level.
	//
	// Any workspace can be a parent: a task nested in a project is that
	// project's issue, and a task nested in a task is a sub-issue of it.
	// Projects are the exception — see validateNew — because Linear has no
	// project-inside-a-project or project-inside-an-issue.
	//
	// Nesting on its own says nothing about which commit the worktrees
	// branch off: a nested workspace still starts from Base (the latest
	// upstream default branch) unless InheritParentBranches is set.
	Parent string

	// InheritParentBranches makes the new workspace branch off the parent
	// workspace's own branch for every repo the parent carries a worktree
	// for, instead of Base. Repos the parent does not carry still use Base,
	// and an explicit BaseByRepo entry wins over the inherited value.
	//
	// This is opt-in rather than implied by Parent. Grouping work under a
	// parent is common, whereas wanting that work to start from the
	// parent's in-progress branch instead of the latest default branch is a
	// specific choice, and doing it silently would strand a workspace on
	// stale commits the user never asked to build on.
	InheritParentBranches bool

	// Phase 7 extras (all optional):
	//
	// BaseByRepo overrides the default Base for specific repos when creating
	// worktrees — used by --from-current to branch off a parent workspace's
	// feature branches instead of origin/HEAD.
	BaseByRepo map[string]string

	// CarryFrom seeds the new workspace's CLAUDE.md with a "Spun off from"
	// header pointing at the named parent workspace.
	CarryFrom *CarryContext

	// GenerateCodeWorkspace, when true, writes a <name>.code-workspace JSON
	// file alongside the workspace dir.
	GenerateCodeWorkspace bool

	// Progress, when non-nil, receives one line per per-repo step (fetch,
	// worktree add) so callers can show "which repo am I on" feedback while
	// New runs. Useful for diagnosing hangs since each step shells out to git.
	Progress io.Writer
}

// CarryContext is the parent-workspace info copied forward when --carry-context
// is set on `arat new`. Only the ticket id is carried; the URL is rendered
// from the service's TicketURL template at write time, so the two can never
// drift apart.
type CarryContext struct {
	ParentName      string // e.g. "abc-123--postal-fix"
	ParentShortName string // e.g. "postal-fix"
	ParentTicket    string // optional, lowercase
}

// shortNameRE constrains workspace short names. Lowercase letters, digits,
// single hyphens. No leading/trailing hyphen, no double hyphens. Length 1..64.
// Disallowing "--" keeps the workspace dir name unambiguous when it's combined
// with a ticket via "<ticket>--<short>".
var shortNameRE = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// shortNameMaxLen caps the short name length.
const shortNameMaxLen = 64

// New creates a workspace on disk: workspaces dir + worktrees + CLAUDE.md +
// claude_workspace/.gitignore. Returns ErrAlreadyExists if the dir is taken.
//
// The caller is expected to validate inputs (cli layer); New double-checks.
func (s *Service) New(ctx context.Context, opts NewOptions) (*Workspace, error) {
	if err := s.validateNew(opts); err != nil {
		return nil, err
	}

	parentDir, parentRef, baseByRepo, err := s.resolveNewParent(ctx, opts)
	if err != nil {
		return nil, err
	}

	repos, err := s.resolveNewRepos(opts)
	if err != nil {
		return nil, err
	}

	dirName := dirNameFor(opts.Kind, opts.ShortName, opts.Ticket)
	branch := BranchName(s.BranchPrefix, opts.ShortName, opts.Ticket)
	full := filepath.Join(parentDir, dirName)

	if _, err := os.Stat(full); err == nil {
		return nil, fmt.Errorf("%w: %s", ErrAlreadyExists, JoinRef(parentRef, dirName))
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("stat %s: %w", full, err)
	}

	if err := os.MkdirAll(full, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", full, err)
	}
	// Failures before any git work only need the directory gone. Once the
	// per-repo jobs start, failure cleanup is unwindCreated below instead:
	// by then there is git state to reverse, not just a directory.
	cleanupDir := func() { _ = os.RemoveAll(full) }

	base := s.Base
	if base == "" {
		base = "origin/HEAD"
	}

	// Pre-flight: every canonical clone must exist and none may already have
	// the branch, before we spawn any work — so a bad repo set fails fast,
	// doesn't race against in-flight fetches, and a branch collision (the
	// same short name in another workspace with an overlapping repo set, or
	// a branch kept by `rm --keep-branches`) surfaces as an arat-level
	// conflict rather than a raw git fatal after some worktrees were made.
	for _, repo := range repos {
		canonical := filepath.Join(s.Root, repo)
		if !dirExists(canonical) {
			cleanupDir()
			return nil, fmt.Errorf("%w: canonical repo %s not found at %s", ErrNotFound, repo, canonical)
		}
		if s.Git.BranchExists(ctx, canonical, branch) {
			cleanupDir()
			return nil, fmt.Errorf("%w: branch %s already exists in %s — used by another workspace, or left behind by one removed with --keep-branches; pick a different name or delete the branch (if git refuses because a worktree uses it, run `git worktree prune` in that repo first)", ErrAlreadyExists, branch, repo)
		}
	}

	// completed records the repos whose worktree add finished, so a failed
	// New can tell state it must report if unwinding fails from repos that
	// never got that far. Mutex-guarded: the errgroup writes concurrently.
	var (
		completedMu sync.Mutex
		completed   = map[string]bool{}
	)

	// unwindCreated reverses what the per-repo jobs created — each worktree
	// removed from its canonical repo, the branch deleted, stale
	// registrations pruned — and then deletes the workspace directory.
	// Without the git unwind, a partial New leaves a branch and a worktree
	// registration in every canonical clone that got that far, and retrying
	// the identical command trips the branch-collision pre-flight with state
	// only `git worktree prune` plus `git branch -D` per repo could clear.
	//
	// It sweeps every repo in the set, not just the completed ones: a job
	// killed mid-add (Ctrl-C) can leave a branch or a registration behind
	// without ever reporting success. That is safe because the pre-flight
	// above proved no repo carried the branch beforehand, so anything
	// matching it now was created by this call. It runs on a context
	// detached from ctx — by the time cleanup runs, ctx is typically
	// already cancelled, and a cancelled cleanup is no cleanup at all.
	// Unwind failures on completed repos ride along on the returned error;
	// on never-completed repos they are the expected "nothing there" noise.
	unwindCreated := func(cause error) error {
		cctx := context.WithoutCancel(ctx)
		var leftovers []string
		for i := len(repos) - 1; i >= 0; i-- {
			repo := repos[i]
			canonical := filepath.Join(s.Root, repo)
			target := filepath.Join(full, repo)
			progressf(opts.Progress, "cleaning up %s…\n", repo)
			wtErr := s.Git.WorktreeRemove(cctx, canonical, target, true)
			// Prune before the branch delete: a registration whose
			// directory died mid-add blocks the delete ("used by worktree")
			// and only prune clears it.
			_ = s.Git.WorktreePrune(cctx, canonical)
			brErr := s.Git.BranchDelete(cctx, canonical, branch, true)
			if completed[repo] { // safe unlocked: the errgroup is done
				if wtErr != nil {
					leftovers = append(leftovers, fmt.Sprintf("worktree %s: %v", target, wtErr))
				}
				if brErr != nil {
					leftovers = append(leftovers, fmt.Sprintf("branch %s in %s: %v", branch, repo, brErr))
				}
			}
		}
		if err := os.RemoveAll(full); err != nil {
			leftovers = append(leftovers, fmt.Sprintf("directory %s: %v", full, err))
		}
		if len(leftovers) > 0 {
			return fmt.Errorf("%w\ncleanup could not undo everything — remove manually:\n  %s", cause, strings.Join(leftovers, "\n  "))
		}
		return cause
	}

	progress := newSyncWriter(opts.Progress)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(newConcurrency)
	for _, repo := range repos {
		g.Go(func() error {
			canonical := filepath.Join(s.Root, repo)
			progressf(progress, "fetching %s…\n", repo)
			// Best-effort fetch; surface errors but only when the worktree add then fails.
			fetchErr := s.Git.Fetch(gctx, canonical)

			repoBase := base
			if alt, ok := baseByRepo[repo]; ok && alt != "" {
				repoBase = alt
			}

			target := filepath.Join(full, repo)
			progressf(progress, "creating worktree %s (base %s)…\n", repo, repoBase)
			if err := s.Git.WorktreeAdd(gctx, canonical, branch, target, repoBase); err != nil {
				if fetchErr != nil {
					return fmt.Errorf("worktree add for %s: %w (preceding fetch error: %v)", repo, err, fetchErr)
				}
				return fmt.Errorf("worktree add for %s: %w", repo, err)
			}
			completedMu.Lock()
			completed[repo] = true
			completedMu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, unwindCreated(err)
	}

	now := s.now()
	if err := writeClaudeMD(full, opts, repos, branch, s.TicketURL, now); err != nil {
		return nil, unwindCreated(err)
	}
	if err := writeClaudeWorkspace(full); err != nil {
		return nil, unwindCreated(err)
	}
	// The marker file is what makes this directory addressable as a
	// workspace when it sits inside a project, so it is written for every
	// workspace arat creates, not only for projects.
	if err := writeMeta(full, Meta{Kind: kindOrTask(opts.Kind)}); err != nil {
		return nil, unwindCreated(err)
	}
	if s.GenerateCodeWorkspace || opts.GenerateCodeWorkspace {
		if err := writeCodeWorkspace(full, dirName, repos); err != nil {
			return nil, unwindCreated(err)
		}
	}

	ws := &Workspace{
		Name:      dirName,
		Ref:       JoinRef(parentRef, dirName),
		Parent:    parentRef,
		Path:      full,
		Kind:      kindOrTask(opts.Kind),
		Ticket:    opts.Ticket,
		ShortName: opts.ShortName,
		Created:   now,
		Repos:     []RepoStatus{},
	}
	if opts.Ticket != "" && s.TicketURL != "" {
		ws.TicketURL = renderTicketURL(s.TicketURL, opts.Ticket)
	}
	for _, repo := range repos {
		repoBase := base
		if alt, ok := baseByRepo[repo]; ok && alt != "" {
			repoBase = alt
		}
		ws.Repos = append(ws.Repos, RepoStatus{
			Name:   repo,
			Path:   filepath.Join(full, repo),
			Branch: branch,
			Base:   repoBase,
		})
	}
	return ws, nil
}

// resolveNewParent locates the directory a new workspace is created in.
//
// It returns the parent directory on disk, the parent's ref (empty at top
// level), and the per-repo base branches to create worktrees from. The bases
// are the caller's BaseByRepo unless InheritParentBranches asks for the
// parent project's branches to be folded in, in which case explicit
// BaseByRepo entries still win.
func (s *Service) resolveNewParent(ctx context.Context, opts NewOptions) (dir, ref string, baseByRepo map[string]string, err error) {
	if opts.Parent == "" {
		if opts.InheritParentBranches {
			return "", "", nil, fmt.Errorf("%w: cannot inherit branches without a parent project", ErrInvalidInput)
		}
		return s.WorkspacesDir, "", opts.BaseByRepo, nil
	}

	parent, err := s.Get(ctx, opts.Parent)
	if err != nil {
		return "", "", nil, err
	}
	if err := errIfMetaBroken(parent); err != nil {
		return "", "", nil, err
	}
	// A single-repo workspace's directory is itself a git worktree, so a
	// child created inside it would live inside the repo: invisible to the
	// tree walk (the worktree is classified as a repo and never recursed
	// into), permanently dirtying the repo, and destroyed unseen by
	// `arat rm --force` on the parent.
	if s.Git.IsWorktree(ctx, parent.Path) {
		return "", "", nil, fmt.Errorf("%w: %s is a single-repo workspace (its directory is a git worktree) and cannot contain other workspaces", ErrInvalidInput, parent.Ref)
	}
	// The read side stops descending at maxDepth, so a workspace created
	// deeper would exist on disk but be invisible to ls, the picker, and —
	// fatally — to the precondition checks of `arat rm --recursive`.
	// Refuse at create time instead of building what we cannot see.
	if strings.Count(parent.Ref, "/")+2 > maxDepth {
		return "", "", nil, fmt.Errorf("%w: nesting deeper than %d levels is not supported (parent %s)", ErrInvalidInput, maxDepth, parent.Ref)
	}
	if !opts.InheritParentBranches {
		return parent.Path, parent.Ref, opts.BaseByRepo, nil
	}

	// Stack on the parent's branch per repo. Repos the parent has no
	// worktree for are absent here and so fall back to Base, and an explicit
	// override still wins.
	merged := make(map[string]string, len(parent.Repos)+len(opts.BaseByRepo))
	for _, r := range parent.Repos {
		if r.Branch != "" {
			merged[r.Name] = r.Branch
		}
	}
	// The user explicitly asked to stack on the parent; if the parent has
	// no branch to offer at all, falling back to the default base silently
	// would do the opposite of what was asked.
	if len(merged) == 0 {
		return "", "", nil, fmt.Errorf("%w: %s has no worktrees to inherit branches from — drop --from-parent or give the parent repos first", ErrInvalidInput, parent.Ref)
	}
	maps.Copy(merged, opts.BaseByRepo)
	return parent.Path, parent.Ref, merged, nil
}

// resolveNewRepos picks the repo set to materialise as worktrees.
//
// A project defaults to none — it exists to group work, and paying the fetch
// and worktree cost of a full repo set for a container is rarely what the
// user wants. A task keeps the established default of default_repos +
// auto_repos_glob.
func (s *Service) resolveNewRepos(opts NewOptions) ([]string, error) {
	if opts.Kind == KindProject {
		return opts.Repos, nil
	}
	return s.ResolveRepos(opts.Repos)
}

// dirNameFor composes the directory name for a new workspace. Projects are
// named for themselves; tasks carry their ticket as a "<ticket>--<short>"
// prefix so the ticket is visible in the path and in the picker.
func dirNameFor(kind Kind, short, ticket string) string {
	if kind == KindProject {
		return short
	}
	return DirName(short, ticket)
}

// kindOrTask maps the zero Kind to KindTask so callers can leave it unset.
func kindOrTask(k Kind) Kind {
	if k == "" {
		return KindTask
	}
	return k
}

func (s *Service) validateNew(opts NewOptions) error {
	if len(opts.ShortName) > shortNameMaxLen || !shortNameRE.MatchString(opts.ShortName) {
		return fmt.Errorf("%w: invalid short name %q: lowercase letters/digits/hyphens, no leading/trailing or double hyphens, max %d chars", ErrInvalidInput, opts.ShortName, shortNameMaxLen)
	}
	if k := kindOrTask(opts.Kind); k != KindTask && k != KindProject {
		return fmt.Errorf("%w: kind %q must be %q or %q", ErrInvalidInput, opts.Kind, KindTask, KindProject)
	}
	// Projects are top-level only. arat's tree mirrors Linear's containment
	// rules, and Linear has neither a project inside a project nor a project
	// inside an issue: a project holds issues, and an issue holds sub-issues.
	// Allowing a nested project would model a shape Linear cannot represent,
	// so a linked workspace tree could never round-trip.
	if opts.Kind == KindProject && opts.Parent != "" {
		return fmt.Errorf("%w: a project cannot be nested inside %s — projects live at the top level and hold workspaces, not the other way round", ErrInvalidInput, opts.Parent)
	}
	if opts.Ticket != "" {
		if opts.Kind == KindProject {
			// Linear projects and initiatives are identified by a slug,
			// not an issue number, so they are attached with
			// Service.LinkLinear rather than encoded in the directory name.
			return fmt.Errorf("%w: a project workspace cannot take a ticket — link it to a Linear project or initiative instead", ErrInvalidInput)
		}
		if s.TicketRE != nil && !s.TicketRE.MatchString(opts.Ticket) {
			return fmt.Errorf("%w: ticket %q does not match the configured ticket_pattern %s", ErrInvalidInput, opts.Ticket, s.TicketRE)
		}
	}
	return nil
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// ResolveRepos returns the repo set to materialize as worktrees.
//
// If explicit is non-empty, it is used verbatim (each must exist at Root/<name>).
// Otherwise: union of DefaultRepos and entries matching AutoReposGlob, filtered
// to those that actually exist as a clone at Root. Order: DefaultRepos first
// (preserving config order), then glob matches sorted.
func (s *Service) ResolveRepos(explicit []string) ([]string, error) {
	if len(explicit) > 0 {
		// Cloned so the caller's slice never aliases the one New iterates —
		// a future in-place dedup here must not mutate the input.
		return append([]string(nil), explicit...), nil
	}
	out, err := s.defaultPlusGlob()
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		// ErrInvalidInput so the CLI exits 2: the fix is in the caller's
		// hands (a flag or a config edit), not in git's or linear's.
		return nil, fmt.Errorf("%w: no repos resolved: configure default_repos or auto_repos_glob, or pass --repos", ErrInvalidInput)
	}
	return out, nil
}

// ListRepoCandidates returns every full clone at Root, with Selected set to
// true for repos that ResolveRepos would return without an explicit list. The
// returned slice is ordered: default_repos first (config order), then glob
// matches sorted, then any other clones at Root sorted by name.
func (s *Service) ListRepoCandidates() ([]RepoCandidate, error) {
	preselected, err := s.defaultPlusGlob()
	if err != nil {
		return nil, err
	}
	selected := make(map[string]struct{}, len(preselected))
	for _, n := range preselected {
		selected[n] = struct{}{}
	}

	entries, err := os.ReadDir(s.Root)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", s.Root, err)
	}
	var others []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if _, ok := selected[name]; ok {
			continue
		}
		if !dirExists(filepath.Join(s.Root, name, ".git")) {
			continue
		}
		others = append(others, name)
	}
	sort.Strings(others)

	// Source labels: a pre-selected candidate is either an explicit
	// default_repos entry or an auto_repos_glob match. Listing which is
	// which in the picker turns "why is this box checked" into something
	// the user can read off the row instead of having to recall config.
	fromDefaults := make(map[string]struct{}, len(s.DefaultRepos))
	for _, r := range s.DefaultRepos {
		fromDefaults[r] = struct{}{}
	}

	out := make([]RepoCandidate, 0, len(preselected)+len(others))
	for _, n := range preselected {
		source := "auto_repos_glob"
		if _, ok := fromDefaults[n]; ok {
			source = "default_repos"
		}
		out = append(out, RepoCandidate{Name: n, Selected: true, Source: source})
	}
	for _, n := range others {
		out = append(out, RepoCandidate{Name: n, Selected: false, Source: "other clone"})
	}
	return out, nil
}

// defaultPlusGlob is the union of DefaultRepos (config order) and
// AutoReposGlob matches (sorted), filtered to those that look like a full
// clone at Root. Used by ResolveRepos and ListRepoCandidates.
func (s *Service) defaultPlusGlob() ([]string, error) {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(s.DefaultRepos))
	for _, r := range s.DefaultRepos {
		if _, ok := seen[r]; ok {
			continue
		}
		if !fileOrDirExists(filepath.Join(s.Root, r, ".git")) {
			continue
		}
		seen[r] = struct{}{}
		out = append(out, r)
	}

	var globMatches []string
	for _, pattern := range s.AutoReposGlob {
		matches, err := filepath.Glob(filepath.Join(s.Root, pattern))
		if err != nil {
			return nil, fmt.Errorf("glob %q: %w", pattern, err)
		}
		for _, m := range matches {
			name := filepath.Base(m)
			if _, ok := seen[name]; ok {
				continue
			}
			if !dirExists(filepath.Join(m, ".git")) {
				continue
			}
			seen[name] = struct{}{}
			globMatches = append(globMatches, name)
		}
	}
	sort.Strings(globMatches)
	out = append(out, globMatches...)
	return out, nil
}

// progressf writes a formatted line to w if w is non-nil. Errors are ignored —
// progress output is best-effort and never fails the operation.
func progressf(w io.Writer, format string, args ...any) {
	if w == nil {
		return
	}
	fmt.Fprintf(w, format, args...)
}

// syncWriter serialises Write calls so concurrent goroutines can share a
// single io.Writer (e.g. os.Stderr or a test bytes.Buffer) without interleaving
// or racing on writers that aren't themselves goroutine-safe.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// newSyncWriter wraps w so concurrent Write calls don't interleave. Returns
// nil when w is nil, so progressf's nil-guard still short-circuits.
func newSyncWriter(w io.Writer) io.Writer {
	if w == nil {
		return nil
	}
	return &syncWriter{w: w}
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func fileOrDirExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
