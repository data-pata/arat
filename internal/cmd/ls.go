package cmd

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/data-pata/arat/internal/workspace"
	"github.com/spf13/cobra"
)

func newLsCmd(s *state) *cobra.Command {
	var flat bool
	c := &cobra.Command{
		Use:   "ls",
		Short: "List workspaces with status markers",
		Long: `List all workspaces under the configured workspaces_dir.

For each workspace, prints each repo's branch and any of:
  *dirty*     working tree has uncommitted changes
  *unpushed*  commits ahead of upstream
  *stashes:N* N stash entries

Workspaces nested inside others are shown indented under their parent. With
--flat, every workspace is listed at the top level under its full ref instead,
which is easier to scan (and to grep) once trees get deep.

With --json, emits an array of workspace objects (path, ticket, repos[],
children[], etc). --flat --json emits one flat array of every workspace with
children omitted — each workspace appears exactly once, at any depth.
`,
		Example: "  arat ls\n  arat ls --flat\n  arat ls --flat --json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := s.loadConfig()
			if err != nil {
				return err
			}
			svc := s.deps.NewService(cfg)
			items, err := svc.List(cmd.Context())
			if err != nil {
				if errors.Is(err, workspace.ErrNoWorkspacesDir) {
					// No workspaces yet is not an error; print an informational note.
					s.writer().JSONRecord([]workspace.Workspace{}, func(out io.Writer) {
						fmt.Fprintf(s.deps.Stderr, "no workspaces yet (workspaces_dir does not exist: %s)\n", cfg.WorkspacesDir)
					})
					return nil
				}
				return &exitErr{code: ExitExternal, err: err}
			}
			if flat {
				flatItems := flattenForLs(items)
				s.writer().JSONRecord(flatItems, func(out io.Writer) { writeLsFlatText(out, flatItems) })
				return nil
			}
			s.writer().JSONRecord(items, func(out io.Writer) { writeLsText(out, items) })
			return nil
		},
	}
	c.Flags().BoolVar(&flat, "flat", false, "list every workspace at the top level under its full ref, instead of as an indented tree")
	return c
}

// flattenForLs is workspace.Flatten with Children stripped: in flat output
// every workspace already appears as its own entry, so keeping the subtree on
// each one would print (and marshal) every nested workspace twice.
func flattenForLs(items []workspace.Workspace) []workspace.Workspace {
	flat := workspace.Flatten(items)
	for i := range flat {
		flat[i].Children = nil
	}
	return flat
}

func writeLsText(out io.Writer, items []workspace.Workspace) {
	if len(items) == 0 {
		fmt.Fprintln(out, "no workspaces")
		return
	}
	for i, ws := range items {
		if i > 0 {
			fmt.Fprintln(out)
		}
		writeWorkspaceText(out, ws, 0)
	}
}

// writeWorkspaceText renders one workspace and, for a workspace with
// children, everything nested below it. Depth drives indentation so the
// printed shape matches the directory shape on disk.
func writeWorkspaceText(out io.Writer, ws workspace.Workspace, depth int) {
	pad := strings.Repeat("  ", depth)
	writeWorkspaceHeader(out, pad, ws, ws.Name)
	writeWorkspaceBody(out, pad+"  ", ws)

	if ws.IsProject() && len(ws.Children) == 0 {
		fmt.Fprintf(out, "%s  (no workspaces yet)\n", pad)
	}

	for _, child := range ws.Children {
		fmt.Fprintln(out)
		writeWorkspaceText(out, child, depth+1)
	}
}

// writeLsFlatText renders every workspace as its own top-level block, headed
// by its full ref. Items are pre-flattened.
func writeLsFlatText(out io.Writer, items []workspace.Workspace) {
	if len(items) == 0 {
		fmt.Fprintln(out, "no workspaces")
		return
	}
	for i, ws := range items {
		if i > 0 {
			fmt.Fprintln(out)
		}
		writeWorkspaceHeader(out, "", ws, ws.Ref)
		writeWorkspaceBody(out, "  ", ws)
	}
}

// writeWorkspaceHeader prints the "── name ──" block header, tagging projects.
func writeWorkspaceHeader(out io.Writer, pad string, ws workspace.Workspace, label string) {
	header := fmt.Sprintf("%s── %s ──", pad, label)
	if ws.IsProject() {
		header += " (project)"
	}
	fmt.Fprintln(out, header)
}

// writeWorkspaceBody prints the ticket/linear/repo lines shared by the tree
// and flat renderings.
func writeWorkspaceBody(out io.Writer, body string, ws workspace.Workspace) {
	if ws.TicketURL != "" {
		fmt.Fprintf(out, "%s%s\n", body, ws.TicketURL)
	}
	if ws.Linear != nil {
		fmt.Fprintf(out, "%slinear %s: %s\n", body, ws.Linear.Kind, linearLabel(*ws.Linear))
	}

	for _, r := range ws.Repos {
		branch := r.Branch
		if branch == "" {
			branch = "?"
		}
		line := fmt.Sprintf("%s%s → %s", body, r.Name, branch)
		if r.Dirty {
			line += " *dirty*"
		}
		if r.Unpushed {
			line += " *unpushed*"
		}
		if r.Stashes > 0 {
			line += fmt.Sprintf(" *stashes:%d*", r.Stashes)
		}
		fmt.Fprintln(out, line)
	}

	if len(ws.Repos) == 0 && !ws.IsProject() {
		fmt.Fprintf(out, "%s(no worktrees)\n", body)
	}
}

// linearLabel renders a Linear reference for humans: its URL when known,
// otherwise the cached name, otherwise the raw slug.
func linearLabel(ref workspace.LinearRef) string {
	switch {
	case ref.Name != "" && ref.URL != "":
		return fmt.Sprintf("%s (%s)", ref.Name, ref.URL)
	case ref.URL != "":
		return ref.URL
	case ref.Name != "":
		return ref.Name
	}
	return ref.ID
}
