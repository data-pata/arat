// Package workspace defines the per-task workspace domain types.
package workspace

import (
	"regexp"
	"strings"
	"time"
)

// Workspace is one feature dir on disk: <workspaces_dir>/<Name>/.
type Workspace struct {
	Name      string       `json:"name"`
	Path      string       `json:"path"`
	Ticket    string       `json:"ticket,omitempty"`
	ShortName string       `json:"short_name"`
	TicketURL string       `json:"ticket_url,omitempty"`
	Created   time.Time    `json:"created"`
	Repos     []RepoStatus `json:"repos"`
}

// RepoStatus is the inspection of one worktree inside a workspace.
type RepoStatus struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Branch   string `json:"branch"` // "" if detached
	Dirty    bool   `json:"dirty"`
	Unpushed bool   `json:"unpushed"`
	Stashes  int    `json:"stashes"`
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
