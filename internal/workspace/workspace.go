// Package workspace defines the per-task workspace domain types.
package workspace

import (
	"regexp"
	"strings"
	"time"
)

// Kind distinguishes the two sorts of workspace.
type Kind string

const (
	// KindTask is a leaf workspace: git worktrees for one unit of work,
	// optionally attached to a ticket. This is what every workspace was
	// before projects existed, and remains the default.
	KindTask Kind = "task"
	// KindProject is a container workspace. It may hold child workspaces
	// (tasks or further projects) and may optionally carry worktrees of its
	// own on a long-lived integration branch.
	KindProject Kind = "project"
)

// Workspace is one workspace dir on disk.
//
// Top-level workspaces live at <workspaces_dir>/<Name>/. A workspace nested
// inside a project lives at <parent path>/<Name>/, so Ref — not Name — is the
// stable identifier across the whole tree.
type Workspace struct {
	// Name is the directory's own name, without any parent path.
	Name string `json:"name"`
	// Ref identifies the workspace within the tree: the slash-joined path
	// from workspaces_dir, e.g. "q3-billing/dunning/abc-20--retry-policy".
	// For a top-level workspace, Ref equals Name.
	Ref string `json:"ref"`
	// Parent is the Ref of the containing project, empty at top level.
	Parent string `json:"parent,omitempty"`
	Path   string `json:"path"`
	Kind   Kind   `json:"kind"`
	// Ticket is the attached issue id for a task workspace, derived from
	// the directory name. Project workspaces use Linear instead.
	Ticket    string `json:"ticket,omitempty"`
	ShortName string `json:"short_name"`
	TicketURL string `json:"ticket_url,omitempty"`
	// Linear is a project workspace's attachment to a Linear project or
	// initiative. Always nil for task workspaces, and nil for a project
	// that has not been linked — attachment is optional.
	Linear  *LinearRef   `json:"linear,omitempty"`
	Created time.Time    `json:"created"`
	Repos   []RepoStatus `json:"repos"`
	// Children are the nested workspaces of a project, sorted by name.
	// Always empty for a task workspace.
	Children []Workspace `json:"children,omitempty"`
}

// IsProject reports whether the workspace is a container for other workspaces.
func (w Workspace) IsProject() bool { return w.Kind == KindProject }

// RepoStatus is the inspection of one worktree inside a workspace.
type RepoStatus struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Branch string `json:"branch"` // "" if detached
	// Base is the ref the branch was created from. Only populated on the
	// result of Service.New — it is not recorded on disk, so inspections
	// (ls) cannot recover it.
	Base     string `json:"base,omitempty"`
	Dirty    bool   `json:"dirty"`
	Unpushed bool   `json:"unpushed"`
	Stashes  int    `json:"stashes"`
}

// RepoCandidate is a repo at <root> that could be added as a worktree to a new
// workspace. Selected is true for repos that would be picked by the
// default+glob resolution (i.e. what `arat new` uses without --repos).
type RepoCandidate struct {
	Name     string `json:"name"`
	Selected bool   `json:"selected"`
}

// ParseName splits a workspace dir name into (ticket, short).
//
// Rules:
//   - If name contains "--", split on the first "--".
//   - If the left side matches the ticket regex, treat as <ticket>--<short>.
//   - Otherwise, the whole name is the short.
//
// The ticket is returned lowercased.
func ParseName(name string, ticketRE *regexp.Regexp) (ticket, short string) {
	short = name
	idx := strings.Index(name, "--")
	if idx <= 0 {
		return "", short
	}
	left := strings.ToLower(name[:idx])
	right := name[idx+2:]
	if ticketRE != nil && ticketRE.MatchString(left) {
		return left, right
	}
	return "", short
}

// BranchName composes the branch name for a workspace.
//
//	prefix=ps short=foo ticket=""        -> "ps--foo"
//	prefix=ps short=foo ticket="abc-1"   -> "ps--foo--abc-1"
func BranchName(prefix, short, ticket string) string {
	b := prefix + "--" + short
	if ticket != "" {
		b += "--" + ticket
	}
	return b
}

// DirName composes the workspace directory name.
//
//	short=foo ticket=""        -> "foo"
//	short=foo ticket="abc-1"   -> "abc-1--foo"
func DirName(short, ticket string) string {
	if ticket == "" {
		return short
	}
	return ticket + "--" + short
}
