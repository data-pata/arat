package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/data-pata/arat/internal/linear"
	"github.com/data-pata/arat/internal/workspace"
	"github.com/spf13/cobra"
)

func newNewCmd(s *state) *cobra.Command {
	var (
		ticket               string
		noTicket             bool
		newTicket            string
		newTicketDescription string
		repos                []string
		fromCurrent          bool
		carryContext         bool
		carrySession         string
		codeWorkspace        bool
		projectMode          bool
		in                   string
	)

	c := &cobra.Command{
		Use:   "new <short-name>",
		Short: "Create a new workspace",
		Long: `Create a new workspace under workspaces_dir, with one git worktree per repo.

By default, branches off origin/HEAD on each repo's canonical clone. The new
branch is named "<branch_prefix>--<short>" or "<branch_prefix>--<short>--<ticket>".

Projects and nesting:
  --project     create a container workspace instead of a leaf. It holds other
                workspaces as subdirectories and gets no worktrees unless
                --repos is given. Projects can contain projects. A project
                attaches to a Linear project or initiative via
                "arat project link", never to an issue.
  --in <ref>    create this workspace inside the named project.

Without --in, the parent is inferred from cwd: running this from anywhere
inside a project creates the new workspace in that project (a task workspace
cannot hold children, so standing in one means "a sibling in the same
project"). Outside any project, the workspace is created at the top level.

When the parent project has a worktree of its own for a repo, the new
workspace branches off the project's branch for that repo rather than
origin/HEAD.

Ticket mode (one of, mutually exclusive):
  --ticket <id>        attach an existing ticket (e.g. abc-123)
  --new-ticket <title> create a new Linear ticket with this title, then attach
  --no-ticket          create without a ticket

When using --new-ticket, --new-ticket-description <body> attaches an optional
description (multi-line supported).

If none are given and stdin is a tty, an interactive chooser opens: skip,
pick from your open Linear issues, or type a title (and optional description)
to create one inline. Outside a tty (AI / pipes), behaves like --no-ticket.

If --repos is omitted: in a tty, an interactive picker opens with
default_repos + auto_repos_glob pre-selected and any other clones at root
available to toggle in. Outside a tty (AI / pipes), falls back to the union of
default_repos and auto_repos_glob.
`,
		Example: `  arat new postal-fix --no-ticket
  arat new postal-fix --ticket abc-123
  arat new postal-fix --new-ticket "Fix postal lookup race"
  arat new postal-fix --ticket abc-123 --repos core-mono,ui-app
  arat new q3-billing --project
  arat new q3-billing --project --repos core-mono
  arat new invoice-pdf --ticket abc-12 --in q3-billing`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			short := args[0]
			if err := validateTicketFlags(ticket, newTicket, newTicketDescription, noTicket); err != nil {
				return &exitErr{code: ExitUsage, err: err}
			}
			if projectMode && (ticket != "" || newTicket != "") {
				return &exitErr{code: ExitUsage, err: errors.New("--project cannot take a ticket: a project links to a Linear project or initiative via `arat project link`, not to an issue")}
			}

			cfg, err := s.loadConfig()
			if err != nil {
				return err
			}
			svc := s.deps.NewService(cfg)

			parent, err := resolveNewParent(cmd.Context(), svc, s.deps.Cwd, in)
			if err != nil {
				return &exitErr{code: ExitUsage, err: err}
			}

			// Interactive repo flow: when --repos wasn't given AND we have a
			// tty, open the multi-select picker pre-populated with the
			// default+glob set. Non-tty (AI/pipe) keeps the union default.
			//
			// Projects skip it: they default to no worktrees, so prompting
			// for a repo set would push the user toward the opposite of the
			// default. A project that wants worktrees names them explicitly.
			if len(repos) == 0 && !projectMode && isInteractive(s.deps) && s.deps.RepoFlow != nil {
				cands, err := svc.ListRepoCandidates()
				if err != nil {
					return &exitErr{code: ExitExternal, err: err}
				}
				if len(cands) > 0 {
					res, err := s.deps.RepoFlow(cmd.Context(), cands, s.deps.Stderr)
					if err != nil {
						return &exitErr{code: ExitExternal, err: err}
					}
					if res.Cancelled {
						return &exitErr{code: ExitUsage, err: errors.New("cancelled")}
					}
					repos = res.Repos
				}
			}

			// --new-ticket: create a ticket up front, non-interactively.
			if newTicket != "" {
				if !cfg.Linear.Enabled {
					return &exitErr{code: ExitUsage, err: errors.New("--new-ticket requires linear (set [linear] enabled = true)")}
				}
				id, err := createTicket(cmd.Context(), s.deps.NewLinear(), cfg.Linear.DefaultTeam, newTicket, newTicketDescription)
				if err != nil {
					return err
				}
				ticket = id
			}

			// Interactive ticket flow: when neither --ticket, --new-ticket,
			// nor --no-ticket was given AND we have a tty, open the chooser.
			// Otherwise default to no-ticket (preserves AI / pipe behaviour).
			// Projects never take an issue, so they skip it entirely.
			if ticket == "" && !noTicket && !projectMode && cfg.Linear.Enabled && isInteractive(s.deps) && s.deps.TicketFlow != nil {
				lc := s.deps.NewLinear()
				if err := lc.Available(cmd.Context()); err == nil {
					res, err := s.deps.TicketFlow(cmd.Context(), lc, cfg.Linear.DefaultTeam, s.deps.Stderr)
					if err != nil {
						return &exitErr{code: ExitExternal, err: err}
					}
					if res.Cancelled {
						return &exitErr{code: ExitUsage, err: errors.New("cancelled")}
					}
					switch {
					case res.Ticket != "":
						ticket = res.Ticket
					case res.NewTitle != "":
						id, err := createTicket(cmd.Context(), lc, cfg.Linear.DefaultTeam, res.NewTitle, res.NewDescription)
						if err != nil {
							return err
						}
						ticket = id
					}
					// Skip path: continue with no ticket.
				}
			}

			newOpts := workspace.NewOptions{
				ShortName:             short,
				Ticket:                strings.ToLower(ticket),
				Repos:                 repos,
				Parent:                parent,
				GenerateCodeWorkspace: codeWorkspace,
			}
			if projectMode {
				newOpts.Kind = workspace.KindProject
			}
			if s.verbose != nil && *s.verbose {
				newOpts.Progress = s.deps.Stderr
			}

			if fromCurrent || carryContext {
				current, err := resolveParentWorkspace(svc, cmd.Context(), s.deps.Cwd)
				if err != nil {
					return &exitErr{code: ExitUsage, err: err}
				}
				if fromCurrent {
					m := make(map[string]string, len(current.Repos))
					for _, r := range current.Repos {
						if r.Branch != "" {
							m[r.Name] = r.Branch
						}
					}
					newOpts.BaseByRepo = m
				}
				if carryContext {
					newOpts.CarryFrom = &workspace.CarryContext{
						ParentName:      current.Ref,
						ParentShortName: current.ShortName,
						ParentTicket:    current.Ticket,
						ParentTicketURL: current.TicketURL,
					}
				}
			}

			ws, err := svc.New(cmd.Context(), newOpts)
			if err != nil {
				return mapNewError(err)
			}

			var carrySrc, carryDst string
			var carryErr error
			if carrySession != "" {
				carrySrc, carryDst, carryErr = svc.MoveSessionFile(cmd.Context(), carrySession, ws.Path)
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
				if carrySession != "" {
					if carryErr != nil {
						fmt.Fprintf(s.deps.Stderr, "  ⚠ carry-session %s: %v\n", carrySession, carryErr)
					} else {
						fmt.Fprintf(s.deps.Stderr, "  carried session: %s → %s\n", carrySrc, carryDst)
					}
				}
			})
			return nil
		},
	}
	c.Flags().StringVar(&ticket, "ticket", "", "ticket id (e.g. abc-123); must match config ticket_pattern")
	c.Flags().BoolVar(&noTicket, "no-ticket", false, "create the workspace without attaching a ticket")
	c.Flags().StringVar(&newTicket, "new-ticket", "", "create a new Linear ticket with this title, then attach it (mutually exclusive with --ticket/--no-ticket)")
	c.Flags().StringVar(&newTicketDescription, "new-ticket-description", "", "description body for --new-ticket (supports multi-line content)")
	c.Flags().StringSliceVar(&repos, "repos", nil, "comma-separated repo names; defaults to default_repos + auto_repos_glob from config")
	c.Flags().BoolVar(&fromCurrent, "from-current", false, "branch new worktrees off the parent workspace's feature branches (parent inferred from cwd) instead of origin/HEAD")
	c.Flags().BoolVar(&carryContext, "carry-context", false, "seed the new CLAUDE.md with a 'Spun off from <parent>' header")
	c.Flags().StringVar(&carrySession, "carry-session", "", "Claude Code session id (e.g. 2bba4a38-93e1-...) to move into the new workspace's project dir so /resume finds it after cd")
	c.Flags().BoolVar(&codeWorkspace, "code-workspace", false, "generate a .code-workspace file (also enabled by config generate_code_workspace)")
	c.Flags().BoolVar(&projectMode, "project", false, "create a project workspace: a container for other workspaces, with no worktrees unless --repos is given")
	c.Flags().StringVar(&in, "in", "", "ref of the project to create this workspace inside (default: the project containing cwd, if any)")
	return c
}

// resolveParentWorkspace finds the workspace that the current cwd is inside.
// Used by --from-current and --carry-context.
func resolveParentWorkspace(svc Service, ctx context.Context, cwdFn func() (string, error)) (*workspace.Workspace, error) {
	if cwdFn == nil {
		return nil, errors.New("--from-current/--carry-context: cwd resolver not configured")
	}
	cwd, err := cwdFn()
	if err != nil {
		return nil, err
	}
	parent, err := svc.WorkspaceAt(ctx, cwd)
	if err != nil {
		return nil, fmt.Errorf("--from-current/--carry-context: %w", err)
	}
	return parent, nil
}

// resolveNewParent decides which project a new workspace is created inside.
//
// An explicit --in always wins. Otherwise the parent is inferred from cwd:
// standing anywhere inside a project (including inside one of its workspaces
// or worktrees) creates the new workspace in that project. Outside any
// project, the workspace is created at the top level, as it always was.
//
// Inference failures are not errors — being outside workspaces_dir entirely
// is the normal case for a top-level `arat new`.
func resolveNewParent(ctx context.Context, svc Service, cwdFn func() (string, error), in string) (string, error) {
	if in != "" {
		project, err := svc.Get(ctx, in)
		if err != nil {
			return "", fmt.Errorf("--in %s: %w", in, err)
		}
		if !project.IsProject() {
			return "", fmt.Errorf("--in %s: %s is a task workspace, not a project", in, project.Ref)
		}
		return project.Ref, nil
	}
	if cwdFn == nil {
		return "", nil
	}
	cwd, err := cwdFn()
	if err != nil {
		return "", nil
	}
	project, err := svc.ProjectAt(ctx, cwd)
	if err != nil || project == nil {
		return "", nil
	}
	return project.Ref, nil
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

// validateTicketFlags enforces mutual exclusion across the ticket-mode
// flags. Each top-level mode (--ticket, --new-ticket, --no-ticket) says
// "do this with the ticket"; combining them is ambiguous. The
// description piggybacks on --new-ticket and is meaningless without it.
func validateTicketFlags(ticket, newTicket, newTicketDescription string, noTicket bool) error {
	chosen := 0
	if ticket != "" {
		chosen++
	}
	if newTicket != "" {
		chosen++
	}
	if noTicket {
		chosen++
	}
	if chosen > 1 {
		return errors.New("--ticket, --new-ticket, and --no-ticket are mutually exclusive")
	}
	if newTicketDescription != "" && newTicket == "" {
		return errors.New("--new-ticket-description requires --new-ticket")
	}
	return nil
}

// createTicket shells out to the Linear client to create an issue with the
// given title (and optional description), using the configured default team
// and the conventional "Backlog" state. Returns the new id (lowercased to
// match arat's storage convention) or a typed exit error.
func createTicket(ctx context.Context, lc LinearClient, team, title, description string) (string, error) {
	if err := lc.Available(ctx); err != nil {
		return "", &exitErr{code: ExitExternal, err: fmt.Errorf("`linear` binary unavailable: %w", err)}
	}
	res, err := lc.IssueCreate(ctx, linear.IssueCreateOptions{
		Title:       title,
		Description: description,
		Team:        team,
		State:       "Backlog",
	})
	if err != nil {
		return "", &exitErr{code: ExitExternal, err: err}
	}
	if res.ID == "" {
		return "", &exitErr{code: ExitExternal, err: errors.New("issue created but identifier could not be parsed from linear output")}
	}
	return strings.ToLower(res.ID), nil
}
