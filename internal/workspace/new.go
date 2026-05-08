package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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
// is set on `arat new`.
type CarryContext struct {
	ParentName      string // e.g. "abc-123--postal-fix"
	ParentShortName string // e.g. "postal-fix"
	ParentTicket    string // optional, lowercase
	ParentTicketURL string // optional fully-rendered URL
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

	repos, err := s.ResolveRepos(opts.Repos)
	if err != nil {
		return nil, err
	}

	dirName := DirName(opts.ShortName, opts.Ticket)
	branch := BranchName(s.BranchPrefix, opts.ShortName, opts.Ticket)
	full := filepath.Join(s.WorkspacesDir, dirName)

	if _, err := os.Stat(full); err == nil {
		return nil, fmt.Errorf("%w: %s", ErrAlreadyExists, dirName)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("stat %s: %w", full, err)
	}

	if err := os.MkdirAll(full, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", full, err)
	}
	// On any failure after this point, clean up the partially-created workspace.
	cleanup := func() { _ = os.RemoveAll(full) }

	base := s.Base
	if base == "" {
		base = "origin/HEAD"
	}

	// Pre-flight: every canonical clone must exist before we spawn any work,
	// so a missing repo fails fast and doesn't race against in-flight fetches.
	for _, repo := range repos {
		if !dirExists(filepath.Join(s.Root, repo)) {
			cleanup()
			return nil, fmt.Errorf("%w: canonical repo %s not found at %s", ErrNotFound, repo, filepath.Join(s.Root, repo))
		}
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
			if alt, ok := opts.BaseByRepo[repo]; ok && alt != "" {
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
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		cleanup()
		return nil, err
	}

	now := s.now()
	if err := writeClaudeMD(full, opts, repos, branch, s.TicketURL, now); err != nil {
		cleanup()
		return nil, err
	}
	if err := writeClaudeWorkspace(full); err != nil {
		cleanup()
		return nil, err
	}
	if s.GenerateCodeWorkspace || opts.GenerateCodeWorkspace {
		if err := writeCodeWorkspace(full, dirName, repos); err != nil {
			cleanup()
			return nil, err
		}
	}

	ws := &Workspace{
		Name:      dirName,
		Path:      full,
		Ticket:    opts.Ticket,
		ShortName: opts.ShortName,
		Created:   now,
	}
	if opts.Ticket != "" && s.TicketURL != "" {
		ws.TicketURL = renderTicketURL(s.TicketURL, opts.Ticket)
	}
	for _, repo := range repos {
		ws.Repos = append(ws.Repos, RepoStatus{
			Name:   repo,
			Path:   filepath.Join(full, repo),
			Branch: branch,
		})
	}
	return ws, nil
}

func (s *Service) validateNew(opts NewOptions) error {
	if len(opts.ShortName) > shortNameMaxLen || !shortNameRE.MatchString(opts.ShortName) {
		return fmt.Errorf("%w: invalid short name %q: lowercase letters/digits/hyphens, no leading/trailing or double hyphens, max %d chars", ErrInvalidInput, opts.ShortName, shortNameMaxLen)
	}
	if opts.Ticket != "" {
		if s.TicketRE != nil && !s.TicketRE.MatchString(opts.Ticket) {
			return fmt.Errorf("%w: ticket %q does not match pattern", ErrInvalidInput, opts.Ticket)
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
		return explicit, nil
	}
	out, err := s.defaultPlusGlob()
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("no repos resolved: configure default_repos or auto_repos_glob, or pass --repos")
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

	out := make([]RepoCandidate, 0, len(preselected)+len(others))
	for _, n := range preselected {
		out = append(out, RepoCandidate{Name: n, Selected: true})
	}
	for _, n := range others {
		out = append(out, RepoCandidate{Name: n, Selected: false})
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
