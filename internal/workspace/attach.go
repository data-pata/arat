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
	Workspace       *Workspace
	Warnings        []AttachWarning
	SessionWarnings []SessionMoveWarning
}

// AttachTicket performs the rename/repair/edit. See type docs above.
func (s *Service) AttachTicket(ctx context.Context, opts AttachOptions) (*AttachResult, error) {
	if opts.Ticket == "" {
		return nil, fmt.Errorf("%w: ticket is required", ErrInvalidInput)
	}
	if s.TicketRE != nil && !s.TicketRE.MatchString(opts.Ticket) {
		return nil, fmt.Errorf("%w: ticket %q does not match pattern", ErrInvalidInput, opts.Ticket)
	}

	current, err := s.Get(ctx, opts.Name)
	if err != nil {
		return nil, err
	}
	if current.IsProject() {
		return nil, fmt.Errorf("%w: %s is a project — link it to a Linear project or initiative with `arat project link` instead", ErrInvalidInput, current.Ref)
	}
	if current.Ticket != "" {
		return nil, &ErrPrecondition{Reasons: []string{
			fmt.Sprintf("workspace %s already has a ticket attached (%s)", current.Ref, current.Ticket),
		}}
	}

	short := current.ShortName
	oldBranch := BranchName(s.BranchPrefix, short, "")
	newBranch := BranchName(s.BranchPrefix, short, opts.Ticket)
	newDirName := DirName(short, opts.Ticket)
	// Rename in place: a nested workspace stays inside its project.
	newPath := filepath.Join(filepath.Dir(current.Path), newDirName)

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

	// 3. Repair worktree registrations in each canonical repo so `git
	//    status` (and later `arat rm`) work from the new path. Two things
	//    matter here: git must be handed each moved worktree's NEW path
	//    (repair without paths only fixes the linked-to-main direction, so
	//    the canonical repo would keep pointing at the old, now-dead path
	//    until `git worktree prune` severs it entirely), and the move takes
	//    every workspace nested under this one along with it, so their
	//    worktrees — possibly in canonical repos this workspace does not
	//    carry itself — need repairing too.
	for canonical, paths := range s.movedWorktreesByCanonical(ctx, newPath) {
		if err := s.Git.WorktreeRepair(ctx, canonical, paths...); err != nil {
			warnings = append(warnings, AttachWarning{
				Reason: "worktree repair failed: " + err.Error(),
			})
		}
	}

	// 4. Edit CLAUDE.md to insert the ticket reference. Preserve user content.
	mdPath := filepath.Join(newPath, "CLAUDE.md")
	if err := updateClaudeMDForAttach(mdPath, short, opts.Ticket, newBranch, s.TicketURL); err != nil {
		warnings = append(warnings, AttachWarning{Reason: "CLAUDE.md update failed: " + err.Error()})
	}

	// 5. Migrate Claude Code session history dirs to the new cwd.
	//    Workspace move on disk is already complete; this only renames
	//    ~/.claude/projects/<encoded> entries so /resume finds your chats
	//    after the workspace dir's been renamed.
	sessionWarnings := s.MoveSessionsForRename(current.Path, newPath)

	updated, err := s.Get(ctx, JoinRef(current.Parent, newDirName))
	if err != nil {
		return nil, err
	}
	return &AttachResult{
		Workspace:       updated,
		Warnings:        warnings,
		SessionWarnings: sessionWarnings,
	}, nil
}

// movedWorktreesByCanonical maps canonical repo path → the worktree paths
// found under root, which is a workspace directory that has just been moved.
// The walk mirrors hydrateContents' classification: a git worktree is a repo
// (recorded, not descended into), a marker directory is a child workspace
// (descended into), anything else is ignored. Descending matters because the
// move took every nested workspace along, and a child may hold worktrees of
// canonical repos the top workspace does not carry itself.
func (s *Service) movedWorktreesByCanonical(ctx context.Context, root string) map[string][]string {
	out := map[string][]string{}
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if s.Git.IsWorktree(ctx, dir) {
			if canonical := s.Git.CanonicalRepoPath(ctx, dir); canonical != "" && canonical != dir {
				out[canonical] = append(out[canonical], dir)
			}
			return
		}
		if depth >= maxDepth {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if !isCandidateSubdir(e) {
				continue
			}
			sub := filepath.Join(dir, e.Name())
			// Only repos and child workspaces are walked; a stray plain
			// directory is not arat's to look inside.
			if s.Git.IsWorktree(ctx, sub) || hasMeta(sub) {
				walk(sub, depth+1)
			}
		}
	}
	walk(root, 0)
	return out
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
