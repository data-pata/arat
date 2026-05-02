package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AttachOptions controls Service.AttachTicket.
type AttachOptions struct {
	Name   string // existing workspace dir name
	Ticket string // ticket id (lowercase, must match TicketRE)
}

// AttachTicket attaches a ticket to an existing ticketless workspace.
// It renames the branches in each worktree, moves the workspace directory,
// repairs the worktree pointers in each canonical repo, and updates
// CLAUDE.md to reference the ticket.
//
// Returns the updated Workspace (with new Name/Path/Ticket/TicketURL).
//
// Errors:
//   - ErrNotFound: the workspace doesn't exist
//   - ErrAlreadyExists: a workspace with the new name already exists
//   - ErrPrecondition: the workspace is already ticketed (use a different
//     workflow if the user wants to re-ticket)
type AttachWarning struct {
	Repo   string
	Branch string
	Reason string
}

// AttachResult is what AttachTicket returns: the updated workspace and any
// non-fatal warnings (e.g. a worktree that had been moved off the original
// branch and so couldn't be auto-renamed).
type AttachResult struct {
	Workspace *Workspace
	Warnings  []AttachWarning
}

// AttachTicket performs the rename/repair/edit. See type docs above.
func (s *Service) AttachTicket(ctx context.Context, opts AttachOptions) (*AttachResult, error) {
	if opts.Ticket == "" {
		return nil, errors.New("ticket is required")
	}
	if s.TicketRE != nil && !s.TicketRE.MatchString(opts.Ticket) {
		return nil, fmt.Errorf("ticket %q does not match pattern", opts.Ticket)
	}

	current, err := s.Get(ctx, opts.Name)
	if err != nil {
		return nil, err
	}
	if current.Ticket != "" {
		return nil, &ErrPrecondition{Reasons: []string{
			fmt.Sprintf("workspace %s already has a ticket attached (%s)", current.Name, current.Ticket),
		}}
	}

	short := current.ShortName
	oldBranch := BranchName(s.BranchPrefix, short, "")
	newBranch := BranchName(s.BranchPrefix, short, opts.Ticket)
	newDirName := DirName(short, opts.Ticket)
	newPath := filepath.Join(s.WorkspacesDir, newDirName)

	if newDirName == current.Name {
		return nil, fmt.Errorf("workspace %s already has the target name", current.Name)
	}
	if _, err := os.Stat(newPath); err == nil {
		return nil, fmt.Errorf("%w: %s", ErrAlreadyExists, newDirName)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("stat %s: %w", newPath, err)
	}

	// 1. Rename branches in each worktree, collecting warnings for any that
	//    aren't on the original ps--<short> branch.
	var warnings []AttachWarning
	for _, r := range current.Repos {
		if r.Branch == newBranch {
			continue // already renamed (re-running attach is idempotent)
		}
		if r.Branch != oldBranch {
			warnings = append(warnings, AttachWarning{
				Repo: r.Name, Branch: r.Branch,
				Reason: fmt.Sprintf("on %q (expected %q) — branch left as-is", r.Branch, oldBranch),
			})
			continue
		}
		if err := s.Git.BranchRename(ctx, r.Path, oldBranch, newBranch); err != nil {
			return nil, fmt.Errorf("rename %s in %s: %w", oldBranch, r.Name, err)
		}
	}

	// 2. Move the workspace directory.
	if err := os.Rename(current.Path, newPath); err != nil {
		return nil, fmt.Errorf("rename %s → %s: %w", current.Path, newPath, err)
	}

	// 3. Repair worktree pointers in each canonical repo so `git status` etc.
	//    work from the new path. We collect the canonical repo from the new
	//    location since the worktrees themselves moved.
	repaired := map[string]struct{}{}
	for _, r := range current.Repos {
		newRepoPath := filepath.Join(newPath, filepath.Base(r.Path))
		canonical := s.Git.CanonicalRepoPath(ctx, newRepoPath)
		if canonical == "" {
			// single-repo workspaces have empty subpath segment
			canonical = s.Git.CanonicalRepoPath(ctx, newPath)
		}
		if canonical == "" || canonical == newRepoPath || canonical == newPath {
			continue
		}
		if _, done := repaired[canonical]; done {
			continue
		}
		if err := s.Git.WorktreeRepair(ctx, canonical); err != nil {
			warnings = append(warnings, AttachWarning{
				Repo: r.Name, Branch: r.Branch, Reason: "worktree repair failed: " + err.Error(),
			})
			continue
		}
		repaired[canonical] = struct{}{}
	}

	// 4. Edit CLAUDE.md to insert the ticket reference. Preserve user content.
	mdPath := filepath.Join(newPath, "CLAUDE.md")
	if err := updateClaudeMDForAttach(mdPath, short, opts.Ticket, newBranch, s.TicketURL); err != nil {
		warnings = append(warnings, AttachWarning{Reason: "CLAUDE.md update failed: " + err.Error()})
	}

	updated, err := s.Get(ctx, newDirName)
	if err != nil {
		return nil, err
	}
	return &AttachResult{Workspace: updated, Warnings: warnings}, nil
}

// updateClaudeMDForAttach replaces the file's header section (everything
// before the first H2 / `## ` heading, or the whole file if no H2 is
// present) with a freshly-templated header that references the ticket.
// User content under H2 headings is preserved.
func updateClaudeMDForAttach(path, short, ticket, branch, ticketURLTmpl string) error {
	existing, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return writeAttachHeader(path, short, ticket, branch, ticketURLTmpl, "")
		}
		return err
	}

	tail := preserveTail(string(existing))
	return writeAttachHeader(path, short, ticket, branch, ticketURLTmpl, tail)
}

// preserveTail returns the body of CLAUDE.md from the first H2 onward (the
// part the user is likely to have edited). Returns "" if no H2 is present.
func preserveTail(content string) string {
	lines := strings.Split(content, "\n")
	for i, ln := range lines {
		if strings.HasPrefix(ln, "## ") {
			return strings.Join(lines[i:], "\n")
		}
	}
	return ""
}

func writeAttachHeader(path, short, ticket, branch, ticketURLTmpl, tail string) error {
	upper := strings.ToUpper(ticket)
	link := upper
	if ticketURLTmpl != "" {
		link = renderTicketURL(ticketURLTmpl, ticket)
	}
	header := fmt.Sprintf(`# %s (%s)

Working copy for [%s](%s).

**Branch**: `+"`%s`"+`

`, short, upper, upper, link, branch)

	body := header
	if tail != "" {
		body += tail
		if !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
	} else {
		body += "## Scope\n\n## Notes\n"
	}
	return os.WriteFile(path, []byte(body), 0o644)
}
