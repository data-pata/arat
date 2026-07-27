package cmd

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/data-pata/arat/internal/linear"
	"github.com/data-pata/arat/internal/workspace"
	"github.com/spf13/cobra"
)

func newProjectCmd(s *state) *cobra.Command {
	c := &cobra.Command{
		Use:   "project",
		Short: "Manage project workspaces and their Linear links",
		Long: `A project workspace is a container: other workspaces live inside it as
subdirectories, and it may itself hold worktrees on a long-lived branch.

Create one with "arat new <name> --project". Create work inside it with
"arat new <short-name>" from within the project, or "--in <project-ref>"
from anywhere.

Linking a project to Linear is optional — a project workspace is fully usable
without it, and so are the workspaces nested inside it.
`,
	}
	c.AddCommand(newProjectLinkCmd(s), newProjectUnlinkCmd(s))
	return c
}

func newProjectLinkCmd(s *state) *cobra.Command {
	var (
		projectName    string
		initiativeName string
	)

	c := &cobra.Command{
		Use:   "link [ref]",
		Short: "Link a project workspace to a Linear project or initiative",
		Long: `Attach a Linear project or initiative to a project workspace.

Without a ref, the target is the project containing the current directory —
running this from inside the project you want to link is the common case.

With --project or --initiative, the value is matched against Linear by slug
id first, then by name (case-insensitive); an ambiguous name is an error
rather than a guess. The two flags are mutually exclusive.

With neither flag, in a terminal, an interactive picker opens over all Linear
projects and initiatives. Outside a terminal (AI / pipes) one of the flags is
required.

The resolved name and URL are cached in the workspace's marker file so
"arat ls" can show them without a network call.
`,
		Example: `  arat project link                    # project from cwd, pick interactively
  arat project link q3-billing
  arat project link q3-billing --project "Q3 Billing"
  arat project link q3-billing --initiative "Payments 2026"`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, query, err := projectLinkTarget(projectName, initiativeName)
			if err != nil {
				return &exitErr{code: ExitUsage, err: err}
			}

			cfg, err := s.loadConfig()
			if err != nil {
				return err
			}
			// Disabled Linear blocks every form of this command, so report
			// it before complaining about missing flags — "add --project"
			// would be advice that cannot work.
			if !cfg.Linear.Enabled {
				return &exitErr{code: ExitUsage, err: errors.New("linear is disabled in config (set [linear] enabled = true)")}
			}
			// No flag and no way to ask: fail as a usage error before any
			// Linear access, so scripts get a stable exit 2.
			if kind == "" && (!isInteractive(s.deps) || s.deps.PickContainer == nil) {
				return &exitErr{code: ExitUsage, err: errors.New("one of --project or --initiative is required (or run in a terminal to pick interactively)")}
			}

			// Resolve the target before any Linear round-trips, so a wrong
			// ref (or not standing in a project) fails fast.
			svc := s.deps.NewService(cfg)
			ref, err := projectRefFromArgsOrCwd(cmd, s, svc, args)
			if err != nil {
				return err
			}

			lc := s.deps.NewLinear()
			if err := lc.Available(cmd.Context()); err != nil {
				return &exitErr{code: ExitExternal, err: fmt.Errorf("`linear` binary unavailable: %w", err)}
			}

			var match linear.Container
			if kind == "" {
				// No flag: interactive pick across both kinds.
				picked, err := pickContainerInteractive(cmd, s, lc)
				if err != nil {
					return err
				}
				match = *picked
			} else {
				containers, err := lc.ContainerList(cmd.Context(), kind)
				if err != nil {
					return &exitErr{code: ExitExternal, err: err}
				}
				match, err = resolveContainer(containers, query)
				if err != nil {
					return &exitErr{code: ExitNotFound, err: err}
				}
			}

			ws, err := svc.LinkLinear(cmd.Context(), workspace.LinkOptions{
				Ref: ref,
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

			s.writer().JSONRecord(ws, func(out io.Writer) {
				fmt.Fprintf(out, "%s\n", ws.Path)
			})
			// Outside the JSON closure so --json does not swallow it.
			fmt.Fprintf(s.deps.Stderr, "linked %s → %s %q (%s)\n", ws.Ref, match.Kind, match.Name, match.URL)
			return nil
		},
	}
	c.Flags().StringVar(&projectName, "project", "", "Linear project slug id or name")
	c.Flags().StringVar(&initiativeName, "initiative", "", "Linear initiative slug id or name")
	return c
}

// projectRefFromArgsOrCwd resolves which project workspace a link/unlink
// targets: the explicit ref when given, otherwise the nearest project
// containing the current directory.
func projectRefFromArgsOrCwd(cmd *cobra.Command, s *state, svc Service, args []string) (string, error) {
	if len(args) == 1 {
		return args[0], nil
	}
	if s.deps.Cwd == nil {
		return "", &exitErr{code: ExitUsage, err: errors.New("no project ref given and cwd resolver not configured — pass the ref explicitly")}
	}
	cwd, err := s.deps.Cwd()
	if err != nil {
		return "", &exitErr{code: ExitUsage, err: err}
	}
	project, err := svc.ProjectAt(cmd.Context(), cwd)
	if err != nil {
		return "", &exitErr{code: ExitUsage, err: fmt.Errorf("no project ref given and %w — pass the ref explicitly", err)}
	}
	if project == nil {
		return "", &exitErr{code: ExitUsage, err: errors.New("no project ref given and the current directory is not inside a project — pass the ref explicitly")}
	}
	return project.Ref, nil
}

func newProjectUnlinkCmd(s *state) *cobra.Command {
	return &cobra.Command{
		Use:   "unlink [ref]",
		Short: "Remove a project workspace's Linear link",
		Long: `Detach the Linear project or initiative from a project workspace.

Without a ref, the target is the project containing the current directory.

The workspace itself and everything nested inside it are untouched. Unlinking
a project that is not linked succeeds and does nothing.
`,
		Example: "  arat project unlink\n  arat project unlink q3-billing",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := s.loadConfig()
			if err != nil {
				return err
			}
			svc := s.deps.NewService(cfg)
			ref, err := projectRefFromArgsOrCwd(cmd, s, svc, args)
			if err != nil {
				return err
			}
			ws, err := svc.UnlinkLinear(cmd.Context(), ref)
			if err != nil {
				return mapProjectError(err)
			}
			s.writer().JSONRecord(ws, func(out io.Writer) {
				fmt.Fprintf(out, "%s\n", ws.Path)
				fmt.Fprintf(s.deps.Stderr, "unlinked %s\n", ws.Ref)
			})
			return nil
		},
	}
}

// projectLinkTarget reports which of --project / --initiative was given and
// the value to look up. Neither is fine — kind comes back empty and the
// caller goes interactive — but both at once is an error.
func projectLinkTarget(projectName, initiativeName string) (kind, query string, err error) {
	switch {
	case projectName != "" && initiativeName != "":
		return "", "", errors.New("--project and --initiative are mutually exclusive")
	case projectName != "":
		return linear.ContainerProject, projectName, nil
	case initiativeName != "":
		return linear.ContainerInitiative, initiativeName, nil
	}
	return "", "", nil
}

// pickContainerInteractive fetches every Linear project and initiative and
// lets the user pick one. The caller has already verified a terminal and a
// wired picker.
func pickContainerInteractive(cmd *cobra.Command, s *state, lc LinearClient) (*linear.Container, error) {
	projects, err := lc.ContainerList(cmd.Context(), linear.ContainerProject)
	if err != nil {
		return nil, &exitErr{code: ExitExternal, err: err}
	}
	initiatives, err := lc.ContainerList(cmd.Context(), linear.ContainerInitiative)
	if err != nil {
		return nil, &exitErr{code: ExitExternal, err: err}
	}

	// Projects first, then initiatives, each sorted by name: linking to a
	// project is the common case, and a stable order makes the list scannable.
	sort.Slice(projects, func(i, j int) bool { return projects[i].Name < projects[j].Name })
	sort.Slice(initiatives, func(i, j int) bool { return initiatives[i].Name < initiatives[j].Name })
	containers := append(projects, initiatives...)
	if len(containers) == 0 {
		return nil, &exitErr{code: ExitNotFound, err: errors.New("no linear projects or initiatives found")}
	}

	picked, err := s.deps.PickContainer(cmd.Context(), containers, s.deps.Stderr)
	if err != nil {
		return nil, &exitErr{code: ExitExternal, err: err}
	}
	if picked == nil {
		return nil, &exitErr{code: ExitUsage, err: errors.New("cancelled")}
	}
	return picked, nil
}

// resolveContainer picks the Linear project or initiative that query refers
// to. An exact slug id wins outright; otherwise the name is matched
// case-insensitively. Multiple name matches are reported rather than guessed
// at, since linking the wrong one is silent and easy to miss afterwards.
func resolveContainer(containers []linear.Container, query string) (linear.Container, error) {
	for _, c := range containers {
		if c.ID == query {
			return c, nil
		}
	}

	var matches []linear.Container
	for _, c := range containers {
		if strings.EqualFold(c.Name, query) {
			matches = append(matches, c)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return linear.Container{}, fmt.Errorf("no linear %s matches %q", containerKindOf(containers), query)
	}

	names := make([]string, 0, len(matches))
	for _, m := range matches {
		names = append(names, fmt.Sprintf("%s (%s)", m.Name, m.ID))
	}
	sort.Strings(names)
	return linear.Container{}, fmt.Errorf("%q matches %d linear entries: %s\nuse the slug id to disambiguate",
		query, len(matches), strings.Join(names, ", "))
}

// containerKindOf reports the kind the candidate list was fetched for, so the
// "not found" message names the right thing. Falls back to a neutral word for
// an empty list.
func containerKindOf(containers []linear.Container) string {
	if len(containers) > 0 {
		return containers[0].Kind
	}
	return "project or initiative"
}

func mapProjectError(err error) error {
	var ambiguous *workspace.ErrAmbiguous
	switch {
	case errors.Is(err, workspace.ErrNotFound):
		return &exitErr{code: ExitNotFound, err: err}
	case errors.As(err, &ambiguous):
		return &exitErr{code: ExitUsage, err: err}
	case errors.Is(err, workspace.ErrInvalidInput):
		return &exitErr{code: ExitUsage, err: err}
	}
	return &exitErr{code: ExitExternal, err: err}
}
