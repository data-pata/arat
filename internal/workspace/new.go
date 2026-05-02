package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

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

	for _, repo := range repos {
		canonical := filepath.Join(s.Root, repo)
		if !dirExists(canonical) {
			cleanup()
			return nil, fmt.Errorf("%w: canonical repo %s not found at %s", ErrNotFound, repo, canonical)
		}
		// Best-effort fetch; surface errors but only when the worktree add then fails.
		fetchErr := s.Git.Fetch(ctx, canonical)

		repoBase := base
		if alt, ok := opts.BaseByRepo[repo]; ok && alt != "" {
			repoBase = alt
		}

		target := filepath.Join(full, repo)
		if err := s.Git.WorktreeAdd(ctx, canonical, branch, target, repoBase); err != nil {
			cleanup()
			if fetchErr != nil {
				return nil, fmt.Errorf("worktree add for %s: %w (preceding fetch error: %v)", repo, err, fetchErr)
			}
			return nil, fmt.Errorf("worktree add for %s: %w", repo, err)
		}
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
	if len(out) == 0 {
		return nil, errors.New("no repos resolved: configure default_repos or auto_repos_glob, or pass --repos")
	}
	return out, nil
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func fileOrDirExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
