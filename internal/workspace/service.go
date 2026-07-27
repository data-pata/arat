package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
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

	// ClaudeProjectsDir is the path to Claude Code's per-cwd session-history
	// root (`~/.claude/projects/` by default). When set, `AttachTicket` and
	// `MoveSessionFile` migrate session jsonls alongside workspace
	// renames/promotions. Leave empty to disable session migration entirely
	// (used in tests that don't care about it).
	ClaudeProjectsDir string
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
	ClaudeProjectsDir     string
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
		ClaudeProjectsDir:     opts.ClaudeProjectsDir,
	}, nil
}

// List enumerates the workspace tree under WorkspacesDir, sorted by name.
//
// Top-level workspaces are returned; a project workspace carries its nested
// workspaces on Children (recursively). Each entry is fully hydrated with its
// repos (subdirs that look like git worktrees) and their inspection.
//
// Use Flatten on the result when you want every workspace regardless of
// depth. Returns ErrNoWorkspacesDir if WorkspacesDir does not exist.
func (s *Service) List(ctx context.Context) ([]Workspace, error) {
	return s.list(ctx, true)
}

// ListShallow is List without per-repo git inspection: every Repos slice
// comes back empty. Used by the interactive picker, where full inspection
// (dirty / unpushed / stash counts on every worktree) would otherwise add a
// multi-second pause before the picker appears.
//
// The tree itself is still walked in full — nesting is derived from the
// workspace marker file, which costs one stat per directory and no git calls,
// so the picker can still offer nested workspaces.
func (s *Service) ListShallow(ctx context.Context) ([]Workspace, error) {
	return s.list(ctx, false)
}

func (s *Service) list(ctx context.Context, inspect bool) ([]Workspace, error) {
	entries, err := os.ReadDir(s.WorkspacesDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNoWorkspacesDir, s.WorkspacesDir)
		}
		return nil, fmt.Errorf("read %s: %w", s.WorkspacesDir, err)
	}

	out := make([]Workspace, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		var modTime time.Time
		if info, err := e.Info(); err == nil {
			modTime = info.ModTime()
		}
		ws, err := s.hydrateDir(ctx, "", e.Name(), filepath.Join(s.WorkspacesDir, e.Name()), modTime, inspect, 0)
		if err != nil {
			return nil, err
		}
		out = append(out, ws)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get returns one workspace, fully hydrated, addressed either by its full ref
// ("q3-billing/abc-12--invoice") or by a bare directory name that is unique
// across the tree ("abc-12--invoice").
//
// Returns ErrNotFound if nothing matches, or *ErrAmbiguous if a bare name
// matches more than one workspace.
func (s *Service) Get(ctx context.Context, ref string) (*Workspace, error) {
	// Fast path: the ref addresses a directory directly. Covers both
	// top-level names and full refs without walking the tree.
	if full, err := s.resolveRefPath(ref); err == nil {
		if info, statErr := os.Stat(full); statErr == nil {
			if !info.IsDir() {
				return nil, fmt.Errorf("%w: %s is not a directory", ErrNotFound, ref)
			}
			clean := cleanRef(ref)
			ws, err := s.hydrateDir(ctx, ParentRef(clean), filepath.Base(full), full, info.ModTime(), true, strings.Count(clean, "/"))
			if err != nil {
				return nil, err
			}
			return &ws, nil
		}
	}

	// Slow path: a bare name that lives somewhere below a project. Walk the
	// tree without git inspection, then fully hydrate whatever matched.
	items, err := s.list(ctx, false)
	if err != nil {
		if errors.Is(err, ErrNoWorkspacesDir) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, ref)
		}
		return nil, err
	}
	found, err := Resolve(items, ref)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(found.Path)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, ref)
	}
	ws, err := s.hydrateDir(ctx, found.Parent, found.Name, found.Path, info.ModTime(), true, strings.Count(found.Ref, "/"))
	if err != nil {
		return nil, err
	}
	return &ws, nil
}

// resolveRefPath maps a workspace ref to an absolute path under
// WorkspacesDir, rejecting refs that would climb out of it.
func (s *Service) resolveRefPath(ref string) (string, error) {
	clean := cleanRef(ref)
	if clean == "" {
		return "", fmt.Errorf("%w: empty workspace name", ErrNotFound)
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%w: %q escapes workspaces_dir", ErrInvalidInput, ref)
	}
	return filepath.Join(s.WorkspacesDir, filepath.FromSlash(clean)), nil
}

// cleanRef normalises a user-supplied ref: trimmed, slash-separated, no
// redundant separators, no leading or trailing slash.
func cleanRef(ref string) string {
	clean := path.Clean(strings.TrimSpace(filepath.ToSlash(ref)))
	if clean == "." || clean == "/" {
		return ""
	}
	return strings.Trim(clean, "/")
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

// hydrateDir builds one Workspace from a directory, recursing into child
// workspaces when the directory is a project.
//
// Classification of a directory's subdirectories, in order:
//
//  1. carries the marker file (MetaFile) -> a child workspace, recursed into
//  2. is a git worktree                  -> one of this workspace's repos
//  3. neither                            -> ignored
//
// The marker is checked first because it is the only signal that survives
// both cases arat has to tell apart: a project's own worktree and a nested
// workspace are both plain subdirectories, and the nested one is the only one
// arat itself created a marker for.
//
// When inspect is false, no git commands run at all: repos are left empty and
// only the marker file drives recursion.
func (s *Service) hydrateDir(ctx context.Context, parentRef, name, full string, created time.Time, inspect bool, depth int) (Workspace, error) {
	meta, err := readMeta(full)
	if err != nil {
		return Workspace{}, err
	}

	ws := Workspace{
		Name:    name,
		Ref:     JoinRef(parentRef, name),
		Parent:  parentRef,
		Path:    full,
		Kind:    KindTask,
		Created: created,
	}
	if meta != nil {
		ws.Kind = meta.Kind
		ws.Linear = meta.Linear
	}

	if ws.IsProject() {
		// A project is named for itself. It attaches to a Linear project or
		// initiative (a slug), never to an issue, so there is no ticket to
		// parse out of the directory name.
		ws.ShortName = name
		return ws, s.hydrateProject(ctx, &ws, inspect, depth)
	}

	ws.Ticket, ws.ShortName = ParseName(name, s.TicketRE)
	if ws.Ticket != "" && s.TicketURL != "" {
		ws.TicketURL = renderTicketURL(s.TicketURL, ws.Ticket)
	}
	if !inspect {
		return ws, nil
	}
	return ws, s.hydrateTaskRepos(ctx, &ws)
}

// hydrateTaskRepos fills ws.Repos for a leaf workspace.
func (s *Service) hydrateTaskRepos(ctx context.Context, ws *Workspace) error {
	// Single-repo workspace: the workspace dir itself is a git worktree.
	// Don't recurse into it (its subdirs aren't separate worktrees).
	if s.Git.IsWorktree(ctx, ws.Path) {
		ws.Repos = append(ws.Repos, s.inspectAt(ctx, "", ws.Path))
		return nil
	}

	subs, err := os.ReadDir(ws.Path)
	if err != nil {
		return fmt.Errorf("read %s: %w", ws.Path, err)
	}
	for _, sub := range subs {
		if !isCandidateSubdir(sub) {
			continue
		}
		repoPath := filepath.Join(ws.Path, sub.Name())
		if !s.Git.IsWorktree(ctx, repoPath) {
			continue
		}
		ws.Repos = append(ws.Repos, s.inspectAt(ctx, sub.Name(), repoPath))
	}
	return nil
}

// hydrateProject fills ws.Children (and, when inspect is set, ws.Repos) for a
// project workspace.
func (s *Service) hydrateProject(ctx context.Context, ws *Workspace, inspect bool, depth int) error {
	if depth >= maxDepth {
		return nil
	}
	subs, err := os.ReadDir(ws.Path)
	if err != nil {
		return fmt.Errorf("read %s: %w", ws.Path, err)
	}
	for _, sub := range subs {
		if !isCandidateSubdir(sub) {
			continue
		}
		subPath := filepath.Join(ws.Path, sub.Name())

		if hasMeta(subPath) {
			var modTime time.Time
			if info, err := sub.Info(); err == nil {
				modTime = info.ModTime()
			}
			child, err := s.hydrateDir(ctx, ws.Ref, sub.Name(), subPath, modTime, inspect, depth+1)
			if err != nil {
				return err
			}
			ws.Children = append(ws.Children, child)
			continue
		}

		if inspect && s.Git.IsWorktree(ctx, subPath) {
			ws.Repos = append(ws.Repos, s.inspectAt(ctx, sub.Name(), subPath))
		}
	}
	sort.Slice(ws.Children, func(i, j int) bool { return ws.Children[i].Name < ws.Children[j].Name })
	return nil
}

// isCandidateSubdir filters the entries of a workspace dir down to those that
// could be a repo worktree or a child workspace: real directories, not
// hidden, and not arat's own scratch dir.
func isCandidateSubdir(e os.DirEntry) bool {
	return e.IsDir() && !strings.HasPrefix(e.Name(), ".") && e.Name() != claudeWorkspaceDir
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
