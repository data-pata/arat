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
		fromParent           bool
	)

	c := &cobra.Command{
		Use:   "new [short-name]",
		Short: "Create a new workspace",
		Long: `Create a new workspace under workspaces_dir, with one git worktree per repo.

By default, branches off origin/HEAD on each repo's canonical clone. The new
branch is named "<branch_prefix>--<short>" or "<branch_prefix>--<short>--<ticket>".

The name is optional when a ticket supplies one. Without a name argument, the
short name is derived from the issue title (lowercased, hyphenated, capped;
the ticket id is prefixed to the directory name as usual). In a terminal the
ticket flow runs first and a name prompt opens pre-filled with the derived
slug — Enter accepts, typing overrides, Esc cancels; skipping the ticket
leaves the prompt empty, where a typed name creates a ticketless workspace
and an empty one cancels. With --ticket or --new-ticket the derivation is
automatic (fetching the title from Linear when needed), so
"arat new --ticket abc-12" works without prompts outside a terminal too.

Projects and nesting, mirroring Linear:
  --project      create a container workspace instead of a leaf. It holds
                 other workspaces as subdirectories and gets no worktrees
                 unless --repos is given. A project attaches to a Linear
                 project or initiative via "arat attach", never to an
                 issue. Projects always live at the top level: Linear has no
                 project inside a project or inside an issue.
  --in <ref>     create this workspace inside the named workspace. A task in
                 a project is that project's issue; a task in a task is a
                 sub-issue of it. Pass "." for the workspace you are in.
  --from-parent  branch off the containing workspace's own branches.

Without --in, the parent is inferred from cwd: running this from anywhere
inside a project creates the new workspace in that project. Standing in a
task means "a sibling in the same project", not "a sub-issue of this task" —
working in a task is the ordinary case, so nesting there is asked for
explicitly with "--in .". Outside any project, the workspace is top level.

Nesting alone does not change where the worktrees start. A nested workspace
still branches off the latest default branch. Pass --from-parent to branch
off the parent's own branch for every repo it carries a worktree for; repos
it does not carry keep the default base.

Ticket mode (one of, mutually exclusive):
  --ticket <id>        attach an existing ticket (e.g. abc-123)
  --new-ticket <title> create a new Linear ticket with this title, then attach
  --no-ticket          create without a ticket

When using --new-ticket, --new-ticket-description <body> attaches an optional
description (multi-line supported).

If none are given and stdin is a tty, an interactive chooser opens: skip,
pick from the team's open issues (yours first, then unassigned, then the
rest), or type a title (and optional description) to create one inline.
Picking an issue nobody is assigned to offers to assign it to you.
Outside a tty (AI / pipes), behaves like --no-ticket.

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
  arat new invoice-pdf --ticket abc-12 --in q3-billing
  arat new pdf-fonts --ticket abc-18 --in q3-billing/abc-12--invoice-pdf
  arat new pdf-fonts --ticket abc-18 --in . --from-parent
  arat new --ticket abc-12             # name derived from the issue title
  arat new                             # pick/create a ticket, then name`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var short string
			if len(args) == 1 {
				short = args[0]
			}
			if err := validateTicketFlags(ticket, newTicket, newTicketDescription, noTicket); err != nil {
				return &exitErr{code: ExitUsage, err: err}
			}
			if projectMode && (ticket != "" || newTicket != "") {
				return &exitErr{code: ExitUsage, err: errors.New("--project cannot take a ticket: a project links to a Linear project or initiative via `arat attach`, not to an issue")}
			}
			if projectMode && in != "" {
				return &exitErr{code: ExitUsage, err: errors.New("--project cannot be combined with --in: projects live at the top level and hold workspaces, not the other way round")}
			}
			if projectMode && fromParent {
				return &exitErr{code: ExitUsage, err: errors.New("--project cannot be combined with --from-parent: a top-level project has no parent to branch off")}
			}
			// Both pick the commit the new worktrees branch off, from
			// different workspaces, so honouring both would mean silently
			// dropping one of them.
			if fromParent && fromCurrent {
				return &exitErr{code: ExitUsage, err: errors.New("--from-parent and --from-current are mutually exclusive: they name different branches to start from")}
			}

			cfg, err := s.loadConfig()
			if err != nil {
				return err
			}
			svc := s.deps.NewService(cfg)

			parent, err := resolveNewParent(cmd.Context(), svc, s.deps.Cwd, in, projectMode)
			if err != nil {
				return &exitErr{code: ExitUsage, err: err}
			}
			// Fail here rather than in the domain layer so the message can
			// name the flags that would fix it.
			if fromParent && parent == "" {
				return &exitErr{code: ExitUsage, err: errors.New("--from-parent: the new workspace has no parent — pass --in <ref> (or --in . for the workspace you are in)")}
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

			// titleForName is the issue title a missing name argument derives
			// from, recorded wherever the ticket's title passes through.
			var titleForName string

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
				titleForName = newTicket
			}

			// Interactive ticket flow: when neither --ticket, --new-ticket,
			// nor --no-ticket was given AND we have a tty, open the chooser.
			// Otherwise default to no-ticket (preserves AI / pipe behaviour).
			// Projects never take an issue, so they skip it entirely.
			if ticket == "" && !noTicket && !projectMode && cfg.Linear.Enabled && isInteractive(s.deps) && s.deps.TicketFlow != nil {
				lc := s.deps.NewLinear()
				if err := lc.Available(cmd.Context()); err == nil {
					res, err := s.deps.TicketFlow(cmd.Context(), lc, TicketFlowOptions{Team: cfg.Linear.DefaultTeam, AllowSkip: true}, s.deps.Stderr)
					if err != nil {
						return &exitErr{code: ExitExternal, err: err}
					}
					if res.Cancelled {
						return &exitErr{code: ExitUsage, err: errors.New("cancelled")}
					}
					switch {
					case res.Ticket != "":
						ticket = res.Ticket
						titleForName = res.TicketTitle
						offerAssign(cmd.Context(), s, lc, ticket, res.TicketUnassigned)
					case res.NewTitle != "":
						id, err := createTicket(cmd.Context(), lc, cfg.Linear.DefaultTeam, res.NewTitle, res.NewDescription)
						if err != nil {
							return err
						}
						ticket = id
						titleForName = res.NewTitle
					}
					// Skip path: continue with no ticket.
				}
			}

			// No name argument: derive one from the issue title, confirmed in
			// a pre-filled prompt when a terminal is available.
			if short == "" {
				if titleForName == "" && ticket != "" && cfg.Linear.Enabled && s.deps.NewLinear != nil {
					t, err := issueTitleFor(cmd.Context(), s.deps.NewLinear(), ticket)
					if err != nil {
						// Interactively the prompt still works, just without a
						// default; a script has no such fallback.
						if !isInteractive(s.deps) || s.deps.NameFlow == nil {
							return &exitErr{code: ExitExternal, err: fmt.Errorf("cannot derive a name from %s: %w — pass a name explicitly", strings.ToUpper(ticket), err)}
						}
						fmt.Fprintf(s.deps.Stderr, "⚠ could not fetch the title of %s: %v\n", strings.ToUpper(ticket), err)
					}
					titleForName = t
				}
				def := workspace.SlugFromTitle(titleForName, ticket)
				switch {
				case isInteractive(s.deps) && s.deps.NameFlow != nil:
					res, err := s.deps.NameFlow(cmd.Context(), def, strings.ToLower(ticket), s.deps.Stderr)
					if err != nil {
						return &exitErr{code: ExitExternal, err: err}
					}
					if res.Cancelled || strings.TrimSpace(res.Name) == "" {
						return &exitErr{code: ExitUsage, err: errors.New("cancelled: no workspace name")}
					}
					short = strings.TrimSpace(res.Name)
				case def != "":
					short = def
				default:
					return &exitErr{code: ExitUsage, err: errors.New("a workspace name is required — pass one, or --ticket/--new-ticket to derive it from the issue title")}
				}
			}

			newOpts := workspace.NewOptions{
				ShortName:             short,
				Ticket:                strings.ToLower(ticket),
				Repos:                 repos,
				Parent:                parent,
				InheritParentBranches: fromParent,
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
			})

			// The summary and any warnings go to stderr in BOTH output
			// modes. Tucking them inside the text closure above would make
			// --json silently swallow failures (a bad --carry-session id
			// would vanish), and the summary is where a wrong parent or a
			// wrong base — the two mistakes nesting makes possible — has to
			// become visible: hence the full ref and the per-repo base.
			created := ws.Ref
			if created == "" {
				created = ws.Name
			}
			fmt.Fprintf(s.deps.Stderr, "created workspace %s\n", created)
			if ws.Kind == workspace.KindProject {
				fmt.Fprintf(s.deps.Stderr, "  kind:   project (holds other workspaces; worktrees are opt-in)\n")
			}
			if ws.TicketURL != "" {
				fmt.Fprintf(s.deps.Stderr, "  ticket: %s\n", ws.TicketURL)
			}
			fmt.Fprintf(s.deps.Stderr, "  repos:  %d\n", len(ws.Repos))
			for _, r := range ws.Repos {
				if r.Base != "" {
					fmt.Fprintf(s.deps.Stderr, "    %s → %s (off %s)\n", r.Name, r.Branch, r.Base)
				} else {
					fmt.Fprintf(s.deps.Stderr, "    %s → %s\n", r.Name, r.Branch)
				}
			}
			if carrySession != "" {
				if carryErr != nil {
					fmt.Fprintf(s.deps.Stderr, "  ⚠ carry-session %s: %v\n", carrySession, carryErr)
				} else {
					fmt.Fprintf(s.deps.Stderr, "  carried session: %s → %s\n", carrySrc, carryDst)
				}
			}
			return nil
		},
	}
	c.Flags().StringVar(&ticket, "ticket", "", "ticket id (e.g. abc-123); must match config ticket_pattern")
	c.Flags().BoolVar(&noTicket, "no-ticket", false, "create the workspace without attaching a ticket")
	c.Flags().StringVar(&newTicket, "new-ticket", "", "create a new Linear ticket with this title, then attach it (mutually exclusive with --ticket/--no-ticket)")
	c.Flags().StringVar(&newTicketDescription, "new-ticket-description", "", "description body for --new-ticket (supports multi-line content)")
	c.Flags().StringSliceVar(&repos, "repos", nil, "comma-separated repo names; defaults to default_repos + auto_repos_glob from config")
	c.Flags().BoolVar(&fromCurrent, "from-current", false, "branch new worktrees off the branches of the workspace you are standing in (a spin-off; contrast --from-parent, which stacks on the workspace this one is created inside)")
	c.Flags().BoolVar(&carryContext, "carry-context", false, "seed the new CLAUDE.md with a 'Spun off from <parent>' header")
	c.Flags().StringVar(&carrySession, "carry-session", "", "Claude Code session id (e.g. 2bba4a38-93e1-...) to move into the new workspace's project dir so /resume finds it after cd")
	c.Flags().BoolVar(&codeWorkspace, "code-workspace", false, "generate a .code-workspace file (also enabled by config generate_code_workspace)")
	c.Flags().BoolVar(&projectMode, "project", false, "create a project workspace: a container for other workspaces, with no worktrees unless --repos is given")
	c.Flags().StringVar(&in, "in", "", "ref of the workspace to create this one inside, or \".\" for the workspace containing cwd (default: the project containing cwd, if any)")
	c.Flags().BoolVar(&fromParent, "from-parent", false, "branch new worktrees off the containing workspace's own branches instead of the default base (contrast --from-current, which branches off the workspace you are standing in)")
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

// resolveNewParent decides which workspace a new one is created inside.
//
// An explicit --in always wins, and names any workspace: a task inside a
// project is that project's issue, a task inside a task is a sub-issue. The
// special value "." means the workspace containing cwd, which is how you ask
// for a sub-issue of the task you are standing in.
//
// Without --in the parent is inferred from cwd, and inference deliberately
// resolves to the nearest *project* rather than the nearest workspace. See
// Service.ProjectAt: standing in a task is the ordinary state of working in
// one, so it means "a sibling", not "a child".
//
// Inference failures are not errors — being outside workspaces_dir entirely
// is the normal case for a top-level `arat new`.
//
// projectMode skips inference altogether: a project is always top level, so
// running `arat new x --project` from inside a project must not try to nest
// it and then fail.
func resolveNewParent(ctx context.Context, svc Service, cwdFn func() (string, error), in string, projectMode bool) (string, error) {
	if projectMode {
		return "", nil
	}
	if in == "." {
		if cwdFn == nil {
			return "", errors.New("--in .: cwd resolver not configured")
		}
		cwd, err := cwdFn()
		if err != nil {
			return "", fmt.Errorf("--in .: %w", err)
		}
		ws, err := svc.WorkspaceAt(ctx, cwd)
		if err != nil {
			return "", fmt.Errorf("--in .: %w", err)
		}
		return ws.Ref, nil
	}
	if in != "" {
		parent, err := svc.Get(ctx, in)
		if err != nil {
			return "", fmt.Errorf("--in %s: %w", in, err)
		}
		return parent.Ref, nil
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

// offerAssign asks whether to self-assign a just-picked unassigned issue and
// does so on a yes. Best-effort by design: an assignment failure is a warning,
// not a reason to abort creating the workspace the user already committed to.
func offerAssign(ctx context.Context, s *state, lc LinearClient, ticket string, unassigned bool) {
	if !unassigned || s.deps.Confirm == nil {
		return
	}
	upper := strings.ToUpper(ticket)
	yes, err := s.deps.Confirm(fmt.Sprintf("%s is unassigned — assign it to you? [y/N] ", upper))
	if err != nil || !yes {
		return
	}
	if err := lc.IssueAssignMe(ctx, ticket); err != nil {
		fmt.Fprintf(s.deps.Stderr, "⚠ could not assign %s: %v\n", upper, err)
		return
	}
	fmt.Fprintf(s.deps.Stderr, "assigned %s to you\n", upper)
}

// issueTitleFor fetches an issue's title for name derivation, verifying the
// `linear` binary first so the error names the actual problem.
func issueTitleFor(ctx context.Context, lc LinearClient, ticket string) (string, error) {
	if err := lc.Available(ctx); err != nil {
		return "", fmt.Errorf("`linear` binary unavailable: %w", err)
	}
	return lc.IssueTitle(ctx, ticket)
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
