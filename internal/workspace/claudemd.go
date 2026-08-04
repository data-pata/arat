package workspace

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
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
		if c.ParentTicket != "" && ticketURL != "" {
			ref = fmt.Sprintf("`%s` ([%s](%s))", c.ParentName, strings.ToUpper(c.ParentTicket), renderTicketURL(ticketURL, c.ParentTicket))
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

%sWorking copy — no ticket attached yet. Once one exists, run `+"`arat attach %s <ticket>`"+`.

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
		repoSection = fmt.Sprintf("**Branch**: `%s`\n**Repos**: %s\n\nWorkspaces created inside this project branch off the latest default branch. Pass `--from-parent` to start them from the branch above instead.\n",
			branch, strings.Join(repos, " "))
	}

	return fmt.Sprintf(`# %s

Project workspace, the arat equivalent of a Linear project. Its issues live in
subdirectories of this one and inherit this file as shared context. Those in
turn may hold sub-issues, nested the same way.

**Started**: %s
%s
Create work inside it with `+"`arat new <short-name>`"+` from this directory, or
`+"`arat new <short-name> --in %s`"+` from anywhere. Link it to a Linear project
or initiative with `+"`arat attach %s`"+`.

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

// updateClaudeMDRepos rewrites the generated "**Repos**:" line of a
// workspace's CLAUDE.md to the given list. The file is the context Claude
// actually reads, so a repo added after creation has to show up there — a
// stale list misinforms the one consumer the file exists for. A missing file
// or a header the user rewrote without the line is nothing to update, not an
// error.
func updateClaudeMDRepos(workspaceDir string, repos []string) error {
	path := filepath.Join(workspaceDir, "CLAUDE.md")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	lines := strings.Split(string(data), "\n")
	for i, ln := range lines {
		if strings.HasPrefix(ln, "**Repos**:") {
			lines[i] = "**Repos**: " + strings.Join(repos, " ")
			return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
		}
	}
	return nil
}

// scratchFiles lists the files inside a workspace's claude_workspace/ scratch
// dir as slash-separated paths relative to that dir, sorted. The generated
// top-level .gitignore is excluded: it is arat's own artifact, so a scratch
// dir holding nothing else counts as empty. A missing scratch dir yields nil.
//
// This feeds Remove's scratch precondition: the content is by contract where
// notes meant to outlive the code go, yet it is ignored by git, so removal is
// the one operation that can destroy it with no recovery path.
func scratchFiles(workspaceDir string) ([]string, error) {
	root := filepath.Join(workspaceDir, claudeWorkspaceDir)
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if path == root && errors.Is(walkErr, fs.ErrNotExist) {
				return fs.SkipAll
			}
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == ".gitignore" {
			return nil
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", root, err)
	}
	sort.Strings(out)
	return out, nil
}
