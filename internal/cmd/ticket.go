package cmd

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/data-pata/arat/internal/linear"
	"github.com/data-pata/arat/internal/workspace"
	"github.com/spf13/cobra"
)

func newTicketCmd(s *state) *cobra.Command {
	c := &cobra.Command{
		Use:   "ticket",
		Short: "Manage tickets (Linear)",
	}
	c.AddCommand(newTicketCreateCmd(s), newTicketAttachCmd(s))
	return c
}

func newTicketAttachCmd(s *state) *cobra.Command {
	return &cobra.Command{
		Use:   "attach <name> <ticket>",
		Short: "Attach a ticket to an existing ticketless workspace",
		Long: `Attach a Linear ticket to a workspace that was created without one.

Renames the workspace directory from "<short>" to "<ticket>--<short>",
renames every worktree's branch from "<prefix>--<short>" to
"<prefix>--<short>--<ticket>", repairs the worktree pointers in each
canonical repo, and updates CLAUDE.md to reference the ticket. User
content under H2 headings (e.g. ## Scope, ## Notes) is preserved.

Refuses if the workspace already has a ticket attached. Worktrees
that have been moved off the original branch (e.g. you checked out a
different branch) are reported as warnings and left alone — fix
those manually.
`,
		Example: `  arat ticket attach my-feat abc-123
  arat ticket attach experimental-spike abc-9999`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := s.loadConfig()
			if err != nil {
				return err
			}
			svc := s.deps.NewService(cfg)
			res, err := svc.AttachTicket(cmd.Context(), workspace.AttachOptions{
				Name:   args[0],
				Ticket: strings.ToLower(args[1]),
			})
			if err != nil {
				return mapAttachError(err)
			}
			fmt.Fprintf(s.deps.Stdout, "%s\n", res.Workspace.Path)
			fmt.Fprintf(s.deps.Stderr, "attached %s → %s\n", strings.ToUpper(args[1]), res.Workspace.Name)
			for _, w := range res.Warnings {
				fmt.Fprintf(s.deps.Stderr, "  ⚠ %s: %s\n", w.Repo, w.Reason)
			}
			return nil
		},
	}
}

func mapAttachError(err error) error {
	var pre *workspace.ErrPrecondition
	switch {
	case errors.Is(err, workspace.ErrNotFound):
		return &exitErr{code: ExitNotFound, err: err}
	case errors.Is(err, workspace.ErrAlreadyExists):
		return &exitErr{code: ExitConflict, err: err}
	case errors.As(err, &pre):
		return &exitErr{code: ExitPrecondition, err: err}
	}
	if strings.Contains(err.Error(), "ticket") || strings.Contains(err.Error(), "match pattern") {
		return &exitErr{code: ExitUsage, err: err}
	}
	return &exitErr{code: ExitExternal, err: err}
}

func newTicketCreateCmd(s *state) *cobra.Command {
	var (
		title       string
		description string
		team        string
		project     string
		state_      string
		labels      []string
	)
	c := &cobra.Command{
		Use:   "create",
		Short: "Create a Linear issue",
		Long: `Create a new issue in Linear via the ` + "`linear`" + ` CLI.

Defaults follow the configured [linear] section: --team falls back to
linear.default_team, --state defaults to "Backlog" (matching the team
linear-cli convention).

Multi-line --description is written through linear's --description-file
to avoid shell-escaping pitfalls.

Prints the new issue identifier on stdout (e.g. ABC-123) for easy
piping into ` + "`arat new`" + `.
`,
		Example: `  arat ticket create --title "Fix the thing" --project "Some Project Name"
  arat ticket create -t "BE: tighten validation" --label BE --label api
  TKT=$(arat ticket create -t "Foo") && arat new foo --ticket "$TKT"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(title) == "" {
				return &exitErr{code: ExitUsage, err: errors.New("--title is required")}
			}
			cfg, err := s.loadConfig()
			if err != nil {
				return err
			}
			if !cfg.Linear.Enabled {
				return &exitErr{code: ExitUsage, err: errors.New("linear is disabled in config (set [linear] enabled = true)")}
			}
			lc := s.deps.NewLinear()
			if !lc.Available(cmd.Context()) {
				return &exitErr{code: ExitExternal, err: errors.New("`linear` binary not found on PATH; install from https://github.com/schpet/linear-cli")}
			}

			if team == "" {
				team = cfg.Linear.DefaultTeam
			}
			if state_ == "" {
				state_ = "Backlog"
			}

			res, err := lc.IssueCreate(cmd.Context(), linear.IssueCreateOptions{
				Title:       title,
				Description: description,
				Team:        team,
				Project:     project,
				State:       state_,
				Labels:      labels,
			})
			if err != nil {
				return &exitErr{code: ExitExternal, err: err}
			}

			s.writer().JSONRecord(map[string]string{"id": res.ID, "raw": res.Raw}, func(out io.Writer) {
				if res.ID != "" {
					fmt.Fprintln(out, res.ID)
				}
			})
			if res.Raw != "" {
				fmt.Fprintln(s.deps.Stderr, res.Raw)
			}
			if res.ID == "" {
				return &exitErr{code: ExitExternal, err: errors.New("issue created but identifier could not be parsed from linear output")}
			}
			return nil
		},
	}
	c.Flags().StringVarP(&title, "title", "t", "", "issue title (required)")
	c.Flags().StringVarP(&description, "description", "d", "", "issue description (multi-line written via --description-file)")
	c.Flags().StringVar(&team, "team", "", "team key (e.g. ABC); defaults to linear.default_team")
	c.Flags().StringVar(&project, "project", "", "project name or slug id")
	c.Flags().StringVarP(&state_, "state", "s", "", "workflow state (default: Backlog)")
	c.Flags().StringSliceVarP(&labels, "label", "l", nil, "label (repeatable)")
	_ = c.MarkFlagRequired("title")
	return c
}
