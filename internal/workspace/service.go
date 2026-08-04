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
	// WorktreePrune clears worktree registrations whose directories are
	// gone. New's failure cleanup uses it to sweep up after a worktree add
	// that was killed mid-flight and so never became removable by path.
	WorktreePrune(ctx context.Context, repoDir string) error
	BranchDelete(ctx context.Context, repoDir, branch string, force bool) error
	BranchRename(ctx context.Context, repoDir, from, to string) error
	BranchExists(ctx context.Context, repoDir, branch string) bool
	// InspectFast reads a worktree's branch and canonical repo path from the
	// filesystem alone (no git subprocess). Both return "" when unreadable.
	InspectFast(dir string) (branch, canonical string)
	// WorktreeRepair fixes worktree registrations after a move. The moved
	// worktrees' new paths must be passed explicitly: without them git can
	// only repair the linked-to-main direction, leaving the canonical repo
	// pointing at the old path.
	WorktreeRepair(ctx context.Context, repoDir string, worktreePaths ...string) error
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
	// root (`~/.claude/projects/` by default). When set, `AttachTicket`,
	// `MoveSessionFile`, and `ForkSessionFile` migrate or copy session jsonls
	// alongside workspace operations. Leave empty to disable session handling
	// entirely (used in tests that don't care about it).
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

// listMode is how much per-repo work a tree walk does. Everything above
// listBare still walks the same marker-driven tree; the levels differ only in
// what each workspace's Repos carry and how many git subprocesses that costs.
type listMode int

const (
	// listBare: no repo information at all. Zero git subprocesses.
	listBare listMode = iota
	// listLight: repo names and branches, read from the filesystem (.git
	// files). Zero git subprocesses.
	listLight
	// listFull: everything, including dirty / unpushed / stash state. Around
	// five git subprocesses per worktree, one of which (`git status`) scales
	// with repo size.
	listFull
)

// Detail selects how much per-repo work List does. The tree itself is always
// walked in full — nesting comes from the marker file at one stat per
// directory — so the levels differ only in what each workspace's Repos carry
// and how many git subprocesses that costs.
type Detail int

const (
	// DetailFull inspects every worktree with git: dirty / unpushed / stash
	// state, around five subprocesses per worktree, one of which
	// (`git status`) scales with repo size. The zero value on purpose — a
	// caller that does not choose gets the complete answer, never silently
	// missing state.
	DetailFull Detail = iota
	// DetailLight reads repo names and branches from the filesystem alone
	// (.git files), no subprocesses. What `arat ls` uses by default; full
	// inspection is opt-in via `arat ls --status`.
	DetailLight
	// DetailBare skips repos entirely: every Repos slice comes back empty.
	// Used by the interactive picker, which renders refs only.
	DetailBare
)

// mode maps the public detail level onto the internal walk mode.
func (d Detail) mode() listMode {
	switch d {
	case DetailLight:
		return listLight
	case DetailBare:
		return listBare
	}
	return listFull
}

// ListOptions controls List. The zero value asks for a full listing.
type ListOptions struct {
	Detail Detail
}

// List enumerates the workspace tree under WorkspacesDir, sorted by name.
//
// Top-level workspaces are returned; a project workspace carries its nested
// workspaces on Children (recursively). What each entry's Repos carry is the
// Detail option's choice — the detail level is one axis of the same walk,
// not a family of methods, so a new level is a new constant rather than a
// new method on every consumer interface and fake.
//
// Use Flatten on the result when you want every workspace regardless of
// depth. Returns ErrNoWorkspacesDir if WorkspacesDir does not exist.
func (s *Service) List(ctx context.Context, opts ListOptions) ([]Workspace, error) {
	return s.list(ctx, opts.Detail.mode())
}

func (s *Service) list(ctx context.Context, mode listMode) ([]Workspace, error) {
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
		ws, err := s.hydrateDir(ctx, "", e.Name(), filepath.Join(s.WorkspacesDir, e.Name()), modTime, mode, 0)
		if err != nil {
			return nil, err
		}
		out = append(out, ws)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get returns one workspace, fully hydrated, addressed either by its full ref
// ("q3-billing/abc-12--invoice"), by a bare directory name that is unique
// across the tree ("abc-12--invoice"), or by the anchored "./<ref>" form that
// matches the ref exactly.
//
// Returns ErrNotFound if nothing matches, or *ErrAmbiguous if a bare name
// matches more than one workspace.
func (s *Service) Get(ctx context.Context, ref string) (*Workspace, error) {
	// Fast path: a multi-segment ref whose directory carries the workspace
	// marker. The marker check is what keeps Get from hydrating arbitrary
	// directories that merely exist under workspaces_dir — a repo worktree
	// ("p/repo-a") or any folder inside one ("foo/repo-a/src") would
	// otherwise read as a workspace, and `arat rm` would happily delete it.
	// The worktree check guards the one way a marker appears without arat
	// writing it: committed at the root of the repo itself.
	//
	// Single-segment refs skip the fast path even though the directory could
	// be statted directly: a bare name must go through Resolve's ambiguity
	// scan so a top-level workspace cannot silently shadow a same-named
	// nested one.
	if clean := cleanRef(ref); strings.Contains(clean, "/") {
		if full, err := s.resolveRefPath(ref); err == nil {
			if info, statErr := os.Stat(full); statErr == nil && info.IsDir() &&
				hasMeta(full) && !s.Git.IsWorktree(ctx, full) {
				ws, err := s.hydrateDir(ctx, ParentRef(clean), filepath.Base(full), full, info.ModTime(), listFull, strings.Count(clean, "/"))
				if err != nil {
					return nil, err
				}
				return &ws, nil
			}
		}
	}

	// Slow path: walk the tree without git inspection, then fully hydrate
	// whatever matched.
	items, err := s.list(ctx, listBare)
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
	ws, err := s.hydrateDir(ctx, found.Parent, found.Name, found.Path, info.ModTime(), listFull, strings.Count(found.Ref, "/"))
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
//  1. is a git worktree                  -> one of this workspace's repos
//  2. carries the marker file (MetaFile) -> a child workspace, recursed into
//  3. neither                            -> ignored
//
// The worktree check runs first because a child workspace directory is never
// itself a worktree, whereas a repo's committed tree can legitimately contain
// a .arat.toml at its root — the marker alone cannot tell those apart.
//
// The mode decides how a worktree is recognised and what its RepoStatus
// carries: listFull asks git and inspects state, listLight reads the .git
// entry from the filesystem (names and branches, no state, no subprocesses),
// listBare skips repos entirely and only the marker drives recursion.
func (s *Service) hydrateDir(ctx context.Context, parentRef, name, full string, created time.Time, mode listMode, depth int) (Workspace, error) {
	// A marker that exists but cannot be read degrades this one workspace
	// instead of failing the whole walk: the fault is carried on MetaError,
	// the workspace reads as a task, and every other workspace stays
	// listable, resolvable, and removable. Failing the walk here would brick
	// `ls`, `go`, and — fatally — the `rm` needed to repair the situation,
	// over a single bad file anywhere in the tree.
	meta, metaErr := readMeta(full)

	ws := Workspace{
		Name:    name,
		Ref:     JoinRef(parentRef, name),
		Parent:  parentRef,
		Path:    full,
		Kind:    KindTask,
		Created: created,
		// Initialised so a workspace with no worktrees (a project, or a
		// shallow listing) marshals as "repos": [] rather than null —
		// JSON consumers index into it unconditionally.
		Repos: []RepoStatus{},
	}
	if metaErr != nil {
		ws.MetaError = metaErr.Error()
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
	} else {
		ws.Ticket, ws.ShortName = ParseName(name, s.TicketRE)
		if ws.Ticket != "" && s.TicketURL != "" {
			ws.TicketURL = renderTicketURL(s.TicketURL, ws.Ticket)
		}
	}
	return ws, s.hydrateContents(ctx, &ws, mode, depth)
}

// hydrateContents fills ws.Children and, when the mode reads repos, ws.Repos.
//
// The same walk serves projects and tasks: both can hold child workspaces
// (a task's children are its sub-issues) and both can hold worktrees, so the
// only thing that separates a subdirectory's two possible roles is the marker
// file, not the kind of the workspace containing it.
func (s *Service) hydrateContents(ctx context.Context, ws *Workspace, mode listMode, depth int) error {
	// Single-repo workspace: the workspace dir itself is a git worktree, so
	// its subdirs are the repo's own source tree rather than anything arat
	// put there. Don't classify them.
	if mode != listBare && s.isWorktreeFor(ctx, mode, ws.Path) {
		ws.Repos = append(ws.Repos, s.repoStatusFor(ctx, mode, "", ws.Path))
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

		// A worktree outranks the marker: a child workspace directory is
		// never itself a worktree (arat always creates the multi-repo
		// layout for children), so a subdir that git recognises is a repo —
		// even when the repo's own committed tree happens to contain a
		// .arat.toml at its root. Classifying that repo as a child
		// workspace would make Remove double-count its worktree. The
		// listBare walk cannot afford the check, so there the marker alone
		// decides; the destructive paths all run with listFull.
		if mode != listBare && s.isWorktreeFor(ctx, mode, subPath) {
			ws.Repos = append(ws.Repos, s.repoStatusFor(ctx, mode, sub.Name(), subPath))
			continue
		}

		if hasMeta(subPath) {
			// Past the depth cap, stop descending but keep walking this
			// level so the workspace's own repos are still found.
			if depth >= maxDepth {
				continue
			}
			var modTime time.Time
			if info, err := sub.Info(); err == nil {
				modTime = info.ModTime()
			}
			child, err := s.hydrateDir(ctx, ws.Ref, sub.Name(), subPath, modTime, mode, depth+1)
			if err != nil {
				return err
			}
			ws.Children = append(ws.Children, child)
		}
	}
	sort.Slice(ws.Children, func(i, j int) bool { return ws.Children[i].Name < ws.Children[j].Name })
	return nil
}

// isWorktreeFor is the mode-appropriate "is this directory a repo worktree"
// check: listFull asks git, listLight settles for the presence of a .git
// entry — the same first step IsWorktree takes before its git call.
func (s *Service) isWorktreeFor(ctx context.Context, mode listMode, dir string) bool {
	if mode == listFull {
		return s.Git.IsWorktree(ctx, dir)
	}
	return fileOrDirExists(filepath.Join(dir, ".git"))
}

// repoStatusFor builds a RepoStatus at the mode's cost level.
func (s *Service) repoStatusFor(ctx context.Context, mode listMode, name, path string) RepoStatus {
	if mode == listFull {
		return s.inspectAt(ctx, name, path)
	}
	branch, canonical := s.Git.InspectFast(path)
	if name == "" {
		// Single-repo layout: the directory is named for the workspace, so
		// the repo's identity comes from its canonical clone.
		if canonical != "" {
			name = filepath.Base(canonical)
		} else {
			name = "(repo)"
		}
	}
	return RepoStatus{Name: name, Path: path, Branch: branch}
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
