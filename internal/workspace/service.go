package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/data-pata/arat/internal/git"
)

// Git is what Service needs from `internal/git`.
type Git interface {
	Inspect(ctx context.Context, dir string) (git.Inspection, error)
	IsWorktree(ctx context.Context, dir string) bool
	CanonicalRepoName(ctx context.Context, dir string) string
	CanonicalRepoPath(ctx context.Context, dir string) string
	Fetch(ctx context.Context, repoDir string) error
	WorktreeAdd(ctx context.Context, repoDir, branch, target, base string) error
	WorktreeRemove(ctx context.Context, repoDir, target string, force bool) error
	BranchDelete(ctx context.Context, repoDir, branch string, force bool) error
	BranchRename(ctx context.Context, repoDir, from, to string) error
	WorktreeRepair(ctx context.Context, repoDir string) error
}

// Service is the workspace-domain entry point used by command handlers.
//
// Use NewService to construct one — the zero value is not usable because
// Git is required.
type Service struct {
	Root                  string
	WorkspacesDir         string
	BranchPrefix          string
	TicketRE              *regexp.Regexp
	TicketURL             string // template; {TICKET}/{TICKET_UPPER}
	DefaultRepos          []string
	AutoReposGlob         []string
	Base                  string           // ref to branch from on `New`; default "origin/HEAD"
	GenerateCodeWorkspace bool             // mirrors config.generate_code_workspace
	Now                   func() time.Time // injected for deterministic CLAUDE.md timestamps
	Git                   Git
}

// ServiceOptions are the inputs to NewService. Mandatory: Root, WorkspacesDir,
// Git. Everything else has sensible zero-value behaviour.
type ServiceOptions struct {
	Root                  string
	WorkspacesDir         string
	BranchPrefix          string
	TicketRE              *regexp.Regexp
	TicketURL             string
	DefaultRepos          []string
	AutoReposGlob         []string
	Base                  string
	GenerateCodeWorkspace bool
	Now                   func() time.Time
	Git                   Git
}

// NewService constructs a Service. Returns an error if a mandatory dep
// (Root, WorkspacesDir, Git) is missing.
func NewService(opts ServiceOptions) (*Service, error) {
	if opts.Root == "" {
		return nil, errors.New("workspace: Root is required")
	}
	if opts.WorkspacesDir == "" {
		return nil, errors.New("workspace: WorkspacesDir is required")
	}
	if opts.Git == nil {
		return nil, errors.New("workspace: Git is required")
	}
	return &Service{
		Root:                  opts.Root,
		WorkspacesDir:         opts.WorkspacesDir,
		BranchPrefix:          opts.BranchPrefix,
		TicketRE:              opts.TicketRE,
		TicketURL:             opts.TicketURL,
		DefaultRepos:          opts.DefaultRepos,
		AutoReposGlob:         opts.AutoReposGlob,
		Base:                  opts.Base,
		GenerateCodeWorkspace: opts.GenerateCodeWorkspace,
		Now:                   opts.Now,
		Git:                   opts.Git,
	}, nil
}

// List enumerates workspaces under WorkspacesDir, sorted by name.
//
// Each entry is fully hydrated with its repos (subdirs that look like git
// worktrees) and their inspection. Returns ErrNoWorkspacesDir if WorkspacesDir
// does not exist.
func (s *Service) List(ctx context.Context) ([]Workspace, error) {
	entries, err := os.ReadDir(s.WorkspacesDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNoWorkspacesDir, s.WorkspacesDir)
		}
		return nil, fmt.Errorf("read %s: %w", s.WorkspacesDir, err)
	}

	out := make([]Workspace, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		var modTime time.Time
		if err == nil {
			modTime = info.ModTime()
		}
		ws, err := s.hydrate(ctx, e.Name(), modTime)
		if err != nil {
			return nil, err
		}
		out = append(out, ws)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get returns the workspace with the given dir name, fully hydrated. Returns
// ErrNotFound if it doesn't exist.
func (s *Service) Get(ctx context.Context, name string) (*Workspace, error) {
	full := filepath.Join(s.WorkspacesDir, name)
	info, err := os.Stat(full)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: %s is not a directory", ErrNotFound, name)
	}
	ws, err := s.hydrate(ctx, name, info.ModTime())
	if err != nil {
		return nil, err
	}
	return &ws, nil
}

// ErrNoWorkspacesDir means the configured workspaces_dir doesn't exist yet.
var ErrNoWorkspacesDir = errors.New("workspaces directory does not exist")

// ErrNotFound means the named workspace doesn't exist.
var ErrNotFound = errors.New("workspace not found")

// ErrAlreadyExists means a workspace with the requested name already exists.
var ErrAlreadyExists = errors.New("workspace already exists")

// ErrInvalidInput marks a caller-supplied value (short name, ticket, etc.)
// that fails validation. Wrapped via %w so callers can map to ExitUsage with
// errors.Is rather than string-matching the message.
var ErrInvalidInput = errors.New("invalid input")

// ErrPrecondition means a safety check failed (dirty / unpushed / stashes).
type ErrPrecondition struct {
	Reasons []string
}

func (e *ErrPrecondition) Error() string {
	return "precondition failed:\n  " + strings.Join(e.Reasons, "\n  ")
}

func (s *Service) hydrate(ctx context.Context, name string, created time.Time) (Workspace, error) {
	full := filepath.Join(s.WorkspacesDir, name)
	ticket, short := ParseName(name, s.TicketRE)
	ws := Workspace{
		Name:      name,
		Path:      full,
		Ticket:    ticket,
		ShortName: short,
		Created:   created,
	}
	if ticket != "" && s.TicketURL != "" {
		ws.TicketURL = renderTicketURL(s.TicketURL, ticket)
	}

	// Single-repo workspace: the workspace dir itself is a git worktree.
	// Don't recurse into it (its subdirs aren't separate worktrees).
	if s.Git.IsWorktree(ctx, full) {
		ws.Repos = append(ws.Repos, s.inspectAt(ctx, "", full))
		return ws, nil
	}

	subs, err := os.ReadDir(full)
	if err != nil {
		return ws, fmt.Errorf("read %s: %w", full, err)
	}
	for _, sub := range subs {
		if !sub.IsDir() || strings.HasPrefix(sub.Name(), ".") || sub.Name() == "claude_workspace" {
			continue
		}
		repoPath := filepath.Join(full, sub.Name())
		if !s.Git.IsWorktree(ctx, repoPath) {
			continue
		}
		ws.Repos = append(ws.Repos, s.inspectAt(ctx, sub.Name(), repoPath))
	}
	return ws, nil
}

// inspectAt builds a RepoStatus for a worktree. If name is empty, it derives
// a name from the canonical repo (or falls back to "(repo)").
func (s *Service) inspectAt(ctx context.Context, name, path string) RepoStatus {
	if name == "" {
		name = s.Git.CanonicalRepoName(ctx, path)
		if name == "" {
			name = "(repo)"
		}
	}
	rs := RepoStatus{Name: name, Path: path}
	ins, err := s.Git.Inspect(ctx, path)
	if err != nil {
		return rs
	}
	rs.Branch = ins.Branch
	rs.Dirty = ins.Dirty
	rs.Unpushed = ins.Unpushed
	rs.Stashes = ins.Stashes
	return rs
}

func renderTicketURL(tmpl, ticket string) string {
	upper := strings.ToUpper(ticket)
	r := strings.NewReplacer("{TICKET}", ticket, "{TICKET_UPPER}", upper)
	return r.Replace(tmpl)
}

