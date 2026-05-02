package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/data-pata/arat/internal/workspace"
	"github.com/spf13/cobra"
)

func newNewCmd(s *state) *cobra.Command {
	var (
		ticket        string
		noTicket      bool
		repos         []string
		fromCurrent   bool
		carryContext  bool
		codeWorkspace bool
	)

	c := &cobra.Command{
		Use:   "new <short-name>",
		Short: "Create a new workspace",
		Long: `Create a new workspace under workspaces_dir, with one git worktree per repo.

By default, branches off origin/HEAD on each repo's canonical clone. The new
branch is named "<branch_prefix>--<short>" or "<branch_prefix>--<short>--<ticket>".

If --ticket is given (e.g. abc-123), the workspace dir is "<ticket>--<short>"
and the CLAUDE.md links to the ticket. If neither --ticket nor --no-ticket is
given, behaves like --no-ticket (interactive ticket selection comes in a later
phase).

If --repos is omitted, uses the union of default_repos and auto_repos_glob from
config (those that actually exist as a clone at root).
`,
		Example: `  arat new postal-fix --no-ticket
  arat new postal-fix --ticket abc-123
  arat new postal-fix --ticket abc-123 --repos core-mono,ui-app`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			short := args[0]
			if ticket != "" && noTicket {
				return &exitErr{code: ExitUsage, err: errors.New("--ticket and --no-ticket are mutually exclusive")}
			}

			cfg, err := s.loadConfig()
			if err != nil {
				return err
			}
			svc := s.deps.NewService(cfg)

			// Interactive ticket flow: when neither --ticket nor --no-ticket
			// was given AND we have a tty, open the chooser. Otherwise
			// default to no-ticket (preserves AI / pipe behaviour).
			if ticket == "" && !noTicket && cfg.Linear.Enabled && isInteractive(s.deps) && s.deps.TicketFlow != nil {
				lc := s.deps.NewLinear()
				if lc.Available(cmd.Context()) {
					res, err := s.deps.TicketFlow(cmd.Context(), lc, cfg.Linear.DefaultTeam, s.deps.Stderr)
					if err != nil {
						return &exitErr{code: ExitExternal, err: err}
					}
					if res.Hint != "" {
						fmt.Fprintf(s.deps.Stderr, "%s\n", res.Hint)
					}
					if res.Cancelled {
						return &exitErr{code: ExitUsage, err: errors.New("cancelled")}
					}
					if res.Ticket != "" {
						ticket = res.Ticket
					}
					// Skip / hint paths: continue with no ticket.
				}
			}

			newOpts := workspace.NewOptions{
				ShortName:             short,
				Ticket:                strings.ToLower(ticket),
				Repos:                 repos,
				GenerateCodeWorkspace: codeWorkspace,
			}

			if fromCurrent || carryContext {
				parent, err := resolveParentWorkspace(svc, cmd.Context(), s.deps.Cwd, cfg.WorkspacesDir)
				if err != nil {
					return &exitErr{code: ExitUsage, err: err}
				}
				if fromCurrent {
					m := make(map[string]string, len(parent.Repos))
					for _, r := range parent.Repos {
						if r.Branch != "" {
							m[r.Name] = r.Branch
						}
					}
					newOpts.BaseByRepo = m
				}
				if carryContext {
					newOpts.CarryFrom = &workspace.CarryContext{
						ParentName:      parent.Name,
						ParentShortName: parent.ShortName,
						ParentTicket:    parent.Ticket,
						ParentTicketURL: parent.TicketURL,
					}
				}
			}

			ws, err := svc.New(cmd.Context(), newOpts)
			if err != nil {
				return mapNewError(err)
			}

			s.writer().JSONRecord(ws, func(out io.Writer) {
				fmt.Fprintf(out, "%s\n", ws.Path)
				fmt.Fprintf(s.deps.Stderr, "created workspace %s\n", ws.Name)
				if ws.TicketURL != "" {
					fmt.Fprintf(s.deps.Stderr, "  ticket: %s\n", ws.TicketURL)
				}
				fmt.Fprintf(s.deps.Stderr, "  repos:  %d\n", len(ws.Repos))
				for _, r := range ws.Repos {
					fmt.Fprintf(s.deps.Stderr, "    %s → %s\n", r.Name, r.Branch)
				}
			})
			return nil
		},
	}
	c.Flags().StringVar(&ticket, "ticket", "", "ticket id (e.g. abc-123); must match config ticket_pattern")
	c.Flags().BoolVar(&noTicket, "no-ticket", false, "create the workspace without attaching a ticket")
	c.Flags().StringSliceVar(&repos, "repos", nil, "comma-separated repo names; defaults to default_repos + auto_repos_glob from config")
	c.Flags().BoolVar(&fromCurrent, "from-current", false, "branch new worktrees off the parent workspace's feature branches (parent inferred from cwd) instead of origin/HEAD")
	c.Flags().BoolVar(&carryContext, "carry-context", false, "seed the new CLAUDE.md with a 'Spun off from <parent>' header")
	c.Flags().BoolVar(&codeWorkspace, "code-workspace", false, "generate a .code-workspace file (also enabled by config generate_code_workspace)")
	return c
}

// resolveParentWorkspace finds the workspace that the current cwd is inside.
// Used by --from-current and --carry-context.
func resolveParentWorkspace(svc Service, ctx context.Context, cwdFn func() (string, error), workspacesDir string) (*Workspace, error) {
	if cwdFn == nil {
		return nil, errors.New("--from-current/--carry-context: cwd resolver not configured")
	}
	cwd, err := cwdFn()
	if err != nil {
		return nil, err
	}
	name, err := workspaceFromCwd(cwd, workspacesDir)
	if err != nil {
		return nil, fmt.Errorf("--from-current/--carry-context: %w", err)
	}
	parent, err := svc.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	return parent, nil
}

func mapNewError(err error) error {
	switch {
	case errors.Is(err, workspace.ErrAlreadyExists):
		return &exitErr{code: ExitConflict, err: err}
	case errors.Is(err, workspace.ErrNotFound):
		return &exitErr{code: ExitNotFound, err: err}
	case errors.Is(err, workspace.ErrInvalidInput):
		return &exitErr{code: ExitUsage, err: err}
	}
	return &exitErr{code: ExitExternal, err: err}
}

// isInteractive reports whether arat is running in a terminal where a TUI
// can prompt the user. Falls back to false if no IsTTY resolver is wired.
func isInteractive(d Deps) bool {
	if d.IsTTY == nil {
		return false
	}
	return d.IsTTY()
}
