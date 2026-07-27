package cmd

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/data-pata/arat/internal/config"
	"github.com/data-pata/arat/internal/linear"
	"github.com/data-pata/arat/internal/workspace"
	"github.com/spf13/cobra"
)

func newAttachCmd(s *state) *cobra.Command {
	var (
		newTitle    string
		description string
	)

	c := &cobra.Command{
		Use:   "attach [ref] [ticket-or-name]",
		Short: "Attach a workspace to its Linear counterpart",
		Long: `Attach a workspace to its Linear counterpart. Which counterpart follows from
the workspace's kind: a task workspace attaches to an issue, a project
workspace to a Linear project or initiative.

Without a ref, the target is the workspace containing the current directory —
attaching the workspace you are standing in is the common case.

Task workspaces:
  arat attach abc-123          attach that issue
  arat attach                  in a terminal: pick from your open issues, or
                               type a title to create one inline
  arat attach --new "<title>"  create an issue in linear.default_team, then
                               attach it (-d adds a description)

Attaching an issue renames the workspace directory to "<ticket>--<short>",
renames every worktree branch to "<prefix>--<short>--<ticket>", repairs the
worktree registrations in each canonical repo, and updates CLAUDE.md. User
content under H2 headings is preserved. Refuses if a ticket is already
attached; worktrees moved off the original branch are warned about and left
alone.

Project workspaces:
  arat attach "Q3 Billing"     match by slug id first, then name
                               (case-insensitive), across all Linear projects
                               and initiatives; an ambiguous name is an error
                               listing the slug ids
  arat attach                  in a terminal: pick from all projects and
                               initiatives
  arat attach --new "<name>"   create a Linear project in linear.default_team,
                               then link it (-d adds a description; to attach
                               a new initiative, create it in Linear first)

Linking caches the name and URL in the workspace's marker file so "arat ls"
shows them without a network call. Re-attaching replaces the previous link;
"arat detach" removes it.
`,
		Example: `  arat attach abc-123                  # workspace inferred from cwd
  arat attach invoice-pdf abc-123      # explicit workspace ref
  arat attach                          # interactive, kind-appropriate
  arat attach --new "Fix postal race"  # create the counterpart, then attach
  arat attach "Lidl in Offers"         # project workspace: link by name
  arat attach lidl 2425cfaeb7b1        # project workspace: link by slug id`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("new") && strings.TrimSpace(newTitle) == "" {
				return &exitErr{code: ExitUsage, err: errors.New("--new needs a non-empty title/name")}
			}
			if description != "" && newTitle == "" {
				return &exitErr{code: ExitUsage, err: errors.New("--description requires --new")}
			}
			// With --new the counterpart is named by the flag, so a second
			// positional has nothing to mean.
			if newTitle != "" && len(args) == 2 {
				return &exitErr{code: ExitUsage, err: errors.New("--new replaces the <ticket-or-name> argument; pass at most a workspace ref")}
			}

			cfg, err := s.loadConfig()
			if err != nil {
				return err
			}
			svc := s.deps.NewService(cfg)

			// Positional grammar: the last argument is the thing to attach,
			// except under --new (where the flag carries it), so a lone
			// argument is a ref only when --new is set.
			var explicitRef, query string
			switch {
			case len(args) == 2:
				explicitRef, query = args[0], args[1]
			case len(args) == 1 && newTitle != "":
				explicitRef = args[0]
			case len(args) == 1:
				query = args[0]
			}

			ws, err := attachTargetWorkspace(cmd, s, svc, explicitRef)
			if err != nil {
				return err
			}

			if ws.IsProject() {
				return runAttachProject(cmd, s, cfg, svc, ws, query, newTitle, description)
			}
			return runAttachTask(cmd, s, cfg, svc, ws, query, newTitle, description)
		},
	}
	c.Flags().StringVar(&newTitle, "new", "", "create the Linear counterpart with this title/name, then attach it")
	c.Flags().StringVarP(&description, "description", "d", "", "description body for --new (multi-line supported)")
	return c
}

// attachTargetWorkspace resolves which workspace attach/detach operates on:
// the explicit ref when given, otherwise the workspace containing cwd.
func attachTargetWorkspace(cmd *cobra.Command, s *state, svc Service, explicitRef string) (*workspace.Workspace, error) {
	if explicitRef != "" {
		ws, err := svc.Get(cmd.Context(), explicitRef)
		if err != nil {
			return nil, mapProjectError(err)
		}
		return ws, nil
	}
	ws, err := workspaceFromCwd(cmd.Context(), svc, s.deps.Cwd)
	if err != nil {
		return nil, &exitErr{code: ExitUsage, err: fmt.Errorf("no workspace ref given and %w — pass the ref explicitly", err)}
	}
	return ws, nil
}

// runAttachTask attaches an issue to a task workspace: the given ticket id,
// a freshly created issue (--new), or one chosen in the interactive flow.
func runAttachTask(cmd *cobra.Command, s *state, cfg *config.Config, svc Service, ws *workspace.Workspace, query, newTitle, description string) error {
	ticket := query
	switch {
	case newTitle != "":
		if !cfg.Linear.Enabled {
			return &exitErr{code: ExitUsage, err: errors.New("--new requires linear (set [linear] enabled = true)")}
		}
		id, err := createTicket(cmd.Context(), s.deps.NewLinear(), cfg.Linear.DefaultTeam, newTitle, description)
		if err != nil {
			return err
		}
		ticket = id
	case ticket == "":
		// Nothing named: pick or compose interactively. Unlike `arat new`,
		// which quietly proceeds ticketless when Linear is unusable, attach
		// was asked to attach — failing loudly is the only honest outcome.
		if !cfg.Linear.Enabled {
			return &exitErr{code: ExitUsage, err: fmt.Errorf("%s is a task workspace and needs a ticket id (linear is disabled in config, so there is nothing to pick from)", ws.Ref)}
		}
		if !isInteractive(s.deps) || s.deps.TicketFlow == nil {
			return &exitErr{code: ExitUsage, err: errors.New("a ticket id is required outside a terminal (or pass --new \"<title>\")")}
		}
		lc := s.deps.NewLinear()
		if err := lc.Available(cmd.Context()); err != nil {
			return &exitErr{code: ExitExternal, err: fmt.Errorf("`linear` binary unavailable: %w", err)}
		}
		res, err := s.deps.TicketFlow(cmd.Context(), lc, TicketFlowOptions{Team: cfg.Linear.DefaultTeam}, s.deps.Stderr)
		if err != nil {
			return &exitErr{code: ExitExternal, err: err}
		}
		switch {
		case res.Cancelled || res.Skip:
			return &exitErr{code: ExitUsage, err: errors.New("cancelled")}
		case res.Ticket != "":
			ticket = res.Ticket
		case res.NewTitle != "":
			id, err := createTicket(cmd.Context(), lc, cfg.Linear.DefaultTeam, res.NewTitle, res.NewDescription)
			if err != nil {
				return err
			}
			ticket = id
		}
	}

	res, err := svc.AttachTicket(cmd.Context(), workspace.AttachOptions{
		Name:   ws.Ref,
		Ticket: strings.ToLower(ticket),
	})
	if err != nil {
		return mapAttachError(err)
	}
	s.writer().JSONRecord(res.Workspace, func(out io.Writer) {
		fmt.Fprintf(out, "%s\n", res.Workspace.Path)
	})
	// Outside the JSON closure so --json does not swallow the confirmation
	// or the warnings.
	fmt.Fprintf(s.deps.Stderr, "attached issue %s → %s\n", strings.ToUpper(ticket), res.Workspace.Ref)
	for _, w := range res.Warnings {
		fmt.Fprintf(s.deps.Stderr, "  ⚠ %s: %s\n", w.Repo, w.Reason)
	}
	for _, w := range res.SessionWarnings {
		where := w.Dir
		if w.File != "" {
			where = w.Dir + "/" + w.File
		}
		fmt.Fprintf(s.deps.Stderr, "  ⚠ claude session %s: %s\n", where, w.Reason)
	}
	return nil
}

// runAttachProject links a project workspace to a Linear project or
// initiative: one resolved from the query, a freshly created project (--new),
// or one chosen in the interactive picker.
func runAttachProject(cmd *cobra.Command, s *state, cfg *config.Config, svc Service, ws *workspace.Workspace, query, newTitle, description string) error {
	if !cfg.Linear.Enabled {
		return &exitErr{code: ExitUsage, err: errors.New("linear is disabled in config (set [linear] enabled = true)")}
	}
	// Nothing named and no way to ask: fail as a usage error before any
	// Linear access, so scripts get a stable exit 2.
	if newTitle == "" && query == "" && (!isInteractive(s.deps) || s.deps.PickContainer == nil) {
		return &exitErr{code: ExitUsage, err: fmt.Errorf("%s is a project workspace and needs a Linear project or initiative — pass its name or slug id, or --new \"<name>\" (or run in a terminal to pick interactively)", ws.Ref)}
	}
	lc := s.deps.NewLinear()
	if err := lc.Available(cmd.Context()); err != nil {
		return &exitErr{code: ExitExternal, err: fmt.Errorf("`linear` binary unavailable: %w", err)}
	}

	var match linear.Container
	switch {
	case newTitle != "":
		if cfg.Linear.DefaultTeam == "" {
			return &exitErr{code: ExitUsage, err: errors.New("--new needs a team to create the Linear project in — set [linear] default_team in config")}
		}
		created, err := lc.ProjectCreate(cmd.Context(), linear.ProjectCreateOptions{
			Name:        newTitle,
			Team:        cfg.Linear.DefaultTeam,
			Description: description,
		})
		if err != nil {
			return &exitErr{code: ExitExternal, err: err}
		}
		fmt.Fprintf(s.deps.Stderr, "created linear project %q (%s)\n", created.Name, created.URL)
		match = created
	case query != "":
		containers, err := fetchAllContainers(cmd, lc)
		if err != nil {
			return err
		}
		match, err = resolveContainer(containers, query, "project or initiative")
		if err != nil {
			return &exitErr{code: ExitNotFound, err: err}
		}
	default:
		picked, err := pickContainerInteractive(cmd, s, lc)
		if err != nil {
			return err
		}
		match = *picked
	}

	linked, err := svc.LinkLinear(cmd.Context(), workspace.LinkOptions{
		Ref: ws.Ref,
		Linear: workspace.LinearRef{
			Kind: match.Kind,
			ID:   match.ID,
			Name: match.Name,
			URL:  match.URL,
		},
	})
	if err != nil {
		return mapProjectError(err)
	}
	s.writer().JSONRecord(linked, func(out io.Writer) {
		fmt.Fprintf(out, "%s\n", linked.Path)
	})
	fmt.Fprintf(s.deps.Stderr, "linked %s → %s %q (%s)\n", linked.Ref, match.Kind, match.Name, match.URL)
	return nil
}

func newDetachCmd(s *state) *cobra.Command {
	return &cobra.Command{
		Use:   "detach [ref]",
		Short: "Remove a workspace's Linear link",
		Long: `Remove a workspace's Linear link.

Without a ref, the target is the workspace containing the current directory.

For a project workspace this removes the stored project/initiative link; the
workspace and everything nested inside it are untouched. Detaching an
unlinked project succeeds and does nothing.

A task workspace's ticket cannot be detached: the ticket id is baked into the
directory name and every branch name, so detaching would mean renaming them
all back. Remove the workspace and recreate it instead.
`,
		Example: "  arat detach\n  arat detach q3-billing",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := s.loadConfig()
			if err != nil {
				return err
			}
			svc := s.deps.NewService(cfg)

			var explicitRef string
			if len(args) == 1 {
				explicitRef = args[0]
			}
			ws, err := attachTargetWorkspace(cmd, s, svc, explicitRef)
			if err != nil {
				return err
			}

			if !ws.IsProject() {
				if ws.Ticket != "" {
					return &exitErr{code: ExitUsage, err: fmt.Errorf("cannot detach ticket %s from %s: the ticket id is part of the directory and branch names, and renaming them back is not supported — remove the workspace and recreate it instead", strings.ToUpper(ws.Ticket), ws.Ref)}
				}
				fmt.Fprintf(s.deps.Stderr, "nothing to detach: %s has no ticket attached\n", ws.Ref)
				return nil
			}

			unlinked, err := svc.UnlinkLinear(cmd.Context(), ws.Ref)
			if err != nil {
				return mapProjectError(err)
			}
			s.writer().JSONRecord(unlinked, func(out io.Writer) {
				fmt.Fprintf(out, "%s\n", unlinked.Path)
			})
			fmt.Fprintf(s.deps.Stderr, "unlinked %s\n", unlinked.Ref)
			return nil
		},
	}
}

// fetchAllContainers returns every Linear project and initiative, the
// candidate set a project workspace can attach to.
func fetchAllContainers(cmd *cobra.Command, lc LinearClient) ([]linear.Container, error) {
	projects, err := lc.ContainerList(cmd.Context(), linear.ContainerProject)
	if err != nil {
		return nil, &exitErr{code: ExitExternal, err: err}
	}
	initiatives, err := lc.ContainerList(cmd.Context(), linear.ContainerInitiative)
	if err != nil {
		return nil, &exitErr{code: ExitExternal, err: err}
	}
	return append(projects, initiatives...), nil
}
