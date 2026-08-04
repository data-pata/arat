package cmd

import (
	"context"
	"errors"
	"fmt"
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

			svc, err := s.service(cfg)
			if err != nil {
				return err
			}
			ref, body, err := splitNoteArgs(cmd.Context(), args, svc, s.deps.Cwd)
			if err != nil {
				return &exitErr{code: ExitUsage, err: err}
			}
			if strings.TrimSpace(body) == "" {
				return &exitErr{code: ExitUsage, err: errors.New("comment body is required")}
			}

			ws, err := svc.Get(cmd.Context(), ref)
			if err != nil {
				var ambiguous *workspace.ErrAmbiguous
				switch {
				case errors.Is(err, workspace.ErrNotFound):
					return &exitErr{code: ExitNotFound, err: err}
				case errors.As(err, &ambiguous):
					return &exitErr{code: ExitUsage, err: err}
				}
				return mapUnclassifiedError(err)
			}
			if ws.Ticket == "" {
				return &exitErr{code: ExitPrecondition, err: fmt.Errorf("workspace %s has no ticket attached; nothing to comment on", ws.Ref)}
			}

			lc := s.deps.NewLinear(cfg)
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

// splitNoteArgs decides whether the first positional is a workspace ref or
// part of the note body. The rule: if the first arg resolves to a workspace,
// treat it as the ref; otherwise infer the workspace from cwd and treat every
// argument as body.
//
// Resolution goes through the service rather than a bare directory check so
// that a nested workspace ("q3-billing/abc-12--invoice") and a bare nested
// name ("abc-12--invoice") both work.
func splitNoteArgs(ctx context.Context, args []string, svc Service, cwd func() (string, error)) (ref, body string, err error) {
	if len(args) == 0 {
		return "", "", errors.New("note text is required")
	}
	if ws, lookupErr := svc.Get(ctx, args[0]); lookupErr == nil {
		if len(args) < 2 {
			return "", "", errors.New("note text is required")
		}
		return ws.Ref, strings.Join(args[1:], " "), nil
	}
	// no ref → infer from cwd
	if cwd == nil {
		return "", "", errors.New("workspace name not given and cwd resolver not configured")
	}
	wd, err := cwd()
	if err != nil {
		return "", "", err
	}
	ws, err := svc.WorkspaceAt(ctx, wd)
	if err != nil {
		return "", "", err
	}
	return ws.Ref, strings.Join(args, " "), nil
}
