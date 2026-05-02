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

// writeClaudeWorkspace creates the claude_workspace/ scratch dir with a
// .gitignore that ignores everything except itself.
func writeClaudeWorkspace(workspaceDir string) error {
	dir := filepath.Join(workspaceDir, "claude_workspace")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir claude_workspace: %w", err)
	}
	return os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*\n!.gitignore\n"), 0o644)
}
