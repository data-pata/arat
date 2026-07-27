package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// writeClaudeMD writes the workspace's CLAUDE.md based on whether a ticket is
// attached.
func writeClaudeMD(workspaceDir string, opts NewOptions, repos []string, branch, ticketURL string, now time.Time) error {
	body := renderClaudeMD(opts, repos, branch, ticketURL, now)
	return os.WriteFile(filepath.Join(workspaceDir, "CLAUDE.md"), []byte(body), 0o644)
}

func renderClaudeMD(opts NewOptions, repos []string, branch, ticketURL string, now time.Time) string {
	date := now.Format("2006-01-02")
	repoList := strings.Join(repos, " ")

	if opts.Kind == KindProject {
		return renderProjectClaudeMD(opts, repos, branch, date)
	}

	carry := ""
	if opts.CarryFrom != nil && opts.CarryFrom.ParentName != "" {
		c := opts.CarryFrom
		ref := "`" + c.ParentName + "`"
		if c.ParentTicket != "" && c.ParentTicketURL != "" {
			ref = fmt.Sprintf("`%s` ([%s](%s))", c.ParentName, strings.ToUpper(c.ParentTicket), c.ParentTicketURL)
		}
		carry = fmt.Sprintf("Spun off from %s.\n\n", ref)
	}

	if opts.Ticket != "" {
		ticketUpper := strings.ToUpper(opts.Ticket)
		link := ticketUpper
		if ticketURL != "" {
			link = renderTicketURL(ticketURL, opts.Ticket)
		}
		return fmt.Sprintf(`# %s (%s)

%sWorking copy for [%s](%s).

**Branch**: `+"`%s`"+`
**Started**: %s
**Repos**: %s

## Scope

## Notes
`, opts.ShortName, ticketUpper, carry, ticketUpper, link, branch, date, repoList)
	}

	return fmt.Sprintf(`# %s

%sWorking copy — no ticket attached yet. Once one exists, run `+"`arat ticket attach %s <ticket>`"+`.

**Branch**: `+"`%s`"+`
**Started**: %s
**Repos**: %s

## Scope

## Notes
`, opts.ShortName, carry, opts.ShortName, branch, date, repoList)
}

// renderProjectClaudeMD is the CLAUDE.md for a project workspace.
//
// A project's CLAUDE.md is the shared context every workspace nested under it
// inherits by sitting below it on disk, so it leads with what the project is
// rather than with branch mechanics.
func renderProjectClaudeMD(opts NewOptions, repos []string, branch, date string) string {
	repoSection := "**Repos**: none — this project groups workspaces only\n"
	if len(repos) > 0 {
		repoSection = fmt.Sprintf("**Branch**: `%s`\n**Repos**: %s\n\nWorkspaces created inside this project branch off the latest default branch. Pass `--from-project` to start them from the branch above instead.\n",
			branch, strings.Join(repos, " "))
	}

	return fmt.Sprintf(`# %s

Project workspace. Child workspaces live in subdirectories of this one and
inherit this file as shared context.

**Started**: %s
%s
Create work inside it with `+"`arat new <short-name>`"+` from this directory, or
`+"`arat new <short-name> --in %s`"+` from anywhere. Link it to a Linear project
or initiative with `+"`arat project link %s`"+`.

## Scope

## Notes
`, opts.ShortName, date, repoSection, opts.ShortName, opts.ShortName)
}

// claudeWorkspaceDir is the per-workspace scratch dir arat creates. It is
// never a repo worktree nor a child workspace, so tree walks skip it by name.
const claudeWorkspaceDir = "claude_workspace"

// writeClaudeWorkspace creates the claude_workspace/ scratch dir with a
// .gitignore that ignores everything except itself.
func writeClaudeWorkspace(workspaceDir string) error {
	dir := filepath.Join(workspaceDir, claudeWorkspaceDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir claude_workspace: %w", err)
	}
	return os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*\n!.gitignore\n"), 0o644)
}
