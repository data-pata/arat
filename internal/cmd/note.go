package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/data-pata/arat/internal/linear"
	"github.com/data-pata/arat/internal/workspace"
	"github.com/spf13/cobra"
)

func newNoteCmd(s *state) *cobra.Command {
	c := &cobra.Command{
		Use:   "note [name] <text...>",
		Short: "Add a note as a comment on the workspace's Linear ticket",
		Long: `Append a comment to the Linear issue attached to a workspace.

If [name] is omitted, infers the workspace from the current directory
(must be inside or under workspaces_dir/<name>/). Errors if the
resolved workspace has no ticket attached.

Multi-line text is forwarded via linear's --body-file. The text can be
several positional args; they're joined with single spaces.
`,
		Example: `  arat note abc-123--postal-fix "first cut: validation tightened"
  arat note "investigation: response shape unchanged"   # uses cwd
  cd ~/git/<org>/feat/abc-123--postal-fix && arat note "fixed in core-mono"`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := s.loadConfig()
			if err != nil {
				return err
			}
			if !cfg.Linear.Enabled {
				return &exitErr{code: ExitUsage, err: errors.New("linear is disabled in config (set [linear] enabled = true)")}
			}

			name, body, err := splitNoteArgs(args, s.deps.Cwd, cfg.WorkspacesDir)
			if err != nil {
				return &exitErr{code: ExitUsage, err: err}
			}
			if strings.TrimSpace(body) == "" {
				return &exitErr{code: ExitUsage, err: errors.New("comment body is required")}
			}

			svc := s.deps.NewService(cfg)
			ws, err := svc.Get(cmd.Context(), name)
			if err != nil {
				if errors.Is(err, workspace.ErrNotFound) {
					return &exitErr{code: ExitNotFound, err: err}
				}
				return &exitErr{code: ExitExternal, err: err}
			}
			if ws.Ticket == "" {
				return &exitErr{code: ExitPrecondition, err: fmt.Errorf("workspace %s has no ticket attached; nothing to comment on", ws.Name)}
			}

			lc := s.deps.NewLinear()
			if err := lc.Available(cmd.Context()); err != nil {
				return &exitErr{code: ExitExternal, err: fmt.Errorf("`linear` binary unavailable: %w", err)}
			}
			if err := lc.CommentAdd(cmd.Context(), linear.CommentAddOptions{
				IssueID: ws.Ticket,
				Body:    body,
			}); err != nil {
				return &exitErr{code: ExitExternal, err: err}
			}
			fmt.Fprintf(s.deps.Stderr, "commented on %s\n", strings.ToUpper(ws.Ticket))
			return nil
		},
	}
	return c
}

// splitNoteArgs decides whether the first positional is a workspace name or
// part of the body. The rule: if the first arg is the name of a directory
// directly under workspacesDir, treat it as the workspace name; otherwise
// infer the workspace from cwd and treat everything as body.
func splitNoteArgs(args []string, cwd func() (string, error), workspacesDir string) (name, body string, err error) {
	if len(args) == 0 {
		return "", "", errors.New("note text is required")
	}
	first := args[0]
	if isWorkspaceDir(workspacesDir, first) {
		if len(args) < 2 {
			return "", "", errors.New("note text is required")
		}
		return first, strings.Join(args[1:], " "), nil
	}
	// no name → infer from cwd
	if cwd == nil {
		return "", "", errors.New("workspace name not given and cwd resolver not configured")
	}
	wd, err := cwd()
	if err != nil {
		return "", "", err
	}
	wsName, err := workspaceFromCwd(wd, workspacesDir)
	if err != nil {
		return "", "", err
	}
	return wsName, strings.Join(args, " "), nil
}

func isWorkspaceDir(workspacesDir, name string) bool {
	if workspacesDir == "" || name == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(workspacesDir, name))
	return err == nil && info.IsDir()
}

func workspaceFromCwd(cwd, workspacesDir string) (string, error) {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", err
	}
	wsAbs, err := filepath.Abs(workspacesDir)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(wsAbs, abs)
	if err != nil {
		return "", err
	}
	if rel == "." || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("not inside a workspace (cwd %s is not under workspaces_dir %s)", abs, wsAbs)
	}
	parts := strings.SplitN(rel, string(os.PathSeparator), 2)
	return parts[0], nil
}
