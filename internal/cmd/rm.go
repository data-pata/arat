package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/data-pata/arat/internal/workspace"
	"github.com/spf13/cobra"
)

func newRmCmd(s *state) *cobra.Command {
	var (
		force        bool
		keepBranches bool
		recursive    bool
	)

	c := &cobra.Command{
		Use:     "rm [name]",
		Aliases: []string{"kill"},
		Short:   "Remove a workspace and its worktrees",
		Long: `Remove a workspace, its git worktrees, and (by default) the feature
branches in each canonical repo.

Takes a ref: either the full path from workspaces_dir
("q3-billing/abc-12--invoice") or a bare name that is unique across the tree.
Without one, opens an interactive picker (fzf when available, with a
bubbletea fallback) sorted by recency — same picker as "arat go".

By default, refuses if any worktree has uncommitted changes or unpushed commits
— pass --force to override. Use --keep-branches to remove the worktrees but
keep the branches in the canonical repos.

Removing a project takes everything nested inside it with it, so that requires
--recursive. --force does not stand in for --recursive: --force is about
discarding changes, --recursive is about discarding whole workspaces. With
--recursive, the worktrees of every nested workspace are checked and cleaned
up too.

Stash entries do not block: stash refs live on the canonical clone and survive
worktree removal, so removal proceeds and a note is printed pointing at the
canonical clone where the stashes can still be recovered.

A non-empty claude_workspace/ also blocks: content there is never committed
or pushed, so removing it is unrecoverable. In a terminal, the content is
listed and you are asked to confirm; outside one, the removal is refused
unless --force is given. The generated .gitignore alone does not count.

Returns exit code 4 (precondition) when safety checks trip without --force, or
when a project still has nested workspaces and --recursive was not given.
`,
		Example: `  arat rm                       # interactive picker
  arat rm abc-123--postal-fix
  arat rm postal-fix --force
  arat rm postal-fix --keep-branches
  arat rm q3-billing/abc-12--invoice
  arat rm q3-billing --recursive`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := s.loadConfig()
			if err != nil {
				return err
			}
			svc, err := s.service(cfg)
			if err != nil {
				return err
			}

			var name string
			if len(args) == 1 {
				name = args[0]
			} else {
				chosen, err := s.pickWorkspaceInteractive(cmd, svc)
				if err != nil {
					return err
				}
				if chosen == nil {
					return nil // user cancelled the picker
				}
				// Picker-mode confirmation: a stray Enter on the picker
				// would otherwise silently nuke whichever workspace was
				// highlighted. Explicit-name `arat rm foo` skips this —
				// typing the full name is itself a confirmation.
				if s.deps.Confirm == nil {
					// A terminal without a confirm impl is a wiring bug in
					// the embedding binary, not a user mistake — exit 1.
					return &exitErr{code: ExitGeneric, err: errors.New("interactive confirm not available (no Confirm impl wired)")}
				}
				ok, err := s.deps.Confirm(rmPrompt(*chosen))
				if err != nil {
					return &exitErr{code: ExitGeneric, err: err}
				}
				if !ok {
					fmt.Fprintln(s.deps.Stderr, "cancelled")
					return nil
				}
				name = chosen.Ref
			}

			opts := workspace.RemoveOptions{
				Name:         name,
				Force:        force,
				KeepBranches: keepBranches,
				Recursive:    recursive,
			}
			res, err := svc.Remove(cmd.Context(), opts)
			// Non-empty scratch dir: claude_workspace content lives nowhere
			// else, so show exactly what is there and ask before retrying
			// with deletion confirmed. Outside a terminal the refusal stands
			// as a precondition, cleared by --force.
			var scratch *workspace.ErrScratchNotEmpty
			if errors.As(err, &scratch) && isInteractive(s.deps) && s.deps.Confirm != nil {
				ok, cerr := s.confirmScratchDeletion(scratch)
				if cerr != nil {
					return cerr
				}
				if !ok {
					fmt.Fprintln(s.deps.Stderr, "cancelled")
					return nil
				}
				opts.DeleteScratch = true
				res, err = svc.Remove(cmd.Context(), opts)
			}
			if err != nil {
				// A failed removal may have torn down part of the workspace
				// already; say how far it got rather than only that one git
				// command failed. Re-running continues from here.
				if res != nil && len(res.RemovedWorktrees) > 0 {
					fmt.Fprintf(s.deps.Stderr, "partial teardown — %d %s already removed:\n",
						len(res.RemovedWorktrees), pluralize(len(res.RemovedWorktrees), "worktree", "worktrees"))
					for _, p := range res.RemovedWorktrees {
						fmt.Fprintf(s.deps.Stderr, "  %s\n", p)
					}
					fmt.Fprintln(s.deps.Stderr, "re-running the same rm continues from here")
				}
				return mapRmError(err)
			}

			fmt.Fprintf(s.deps.Stderr, "removed workspace %s\n", name)
			if res != nil {
				// A recursive removal destroys workspaces the user did not
				// name; list them so what went is on record.
				for _, ref := range res.Removed {
					if ref != name {
						fmt.Fprintf(s.deps.Stderr, "  removed nested workspace %s\n", ref)
					}
				}
				for _, w := range res.Warnings {
					fmt.Fprintf(s.deps.Stderr, "  ⚠ %s\n", w)
				}
				for _, sr := range res.StashedRepos {
					fmt.Fprintf(s.deps.Stderr,
						"  note: %d stash %s preserved in %s (git -C %s stash list)\n",
						sr.Stashes, pluralize(sr.Stashes, "entry", "entries"), sr.CanonicalRepo, sr.CanonicalRepo)
				}
			}
			warnIfCwdRemoved(s)
			if s.jsonOut && res != nil {
				s.writer().JSONRecord(res, func(io.Writer) {})
			}
			return nil
		},
	}
	c.Flags().BoolVarP(&force, "force", "f", false, "remove even if dirty or unpushed")
	c.Flags().BoolVar(&keepBranches, "keep-branches", false, "do not delete the branches when removing worktrees")
	c.Flags().BoolVar(&recursive, "recursive", false, "also remove the workspaces nested inside this one")
	return c
}

// warnIfCwdRemoved notes when the shell's working directory no longer exists
// after the removal — running `arat rm` from inside the workspace being
// removed is common, and the resulting getcwd failures are cryptic without a
// pointer to the cause.
func warnIfCwdRemoved(s *state) {
	if s.deps.Cwd == nil {
		return
	}
	cwd, err := s.deps.Cwd()
	if err != nil {
		fmt.Fprintln(s.deps.Stderr, "note: your current directory was inside the removed workspace — cd out of it")
		return
	}
	if _, err := os.Stat(cwd); err != nil {
		fmt.Fprintln(s.deps.Stderr, "note: your current directory was removed — cd out of it")
	}
}

// rmPrompt is the picker-mode confirmation. When the workspace holds others
// it spells out how many go with it, because the directory name alone does
// not convey that removing it removes everything underneath.
func rmPrompt(ws workspace.Workspace) string {
	if n := len(workspace.Descendants(ws)); n > 0 {
		kind := "workspace"
		if ws.IsProject() {
			kind = "project"
		}
		return fmt.Sprintf("Remove %s %q and the %d %s nested in it? [y/N]: ",
			kind, ws.Ref, n, pluralize(n, "workspace", "workspaces"))
	}
	return fmt.Sprintf("Remove workspace %q? [y/N]: ", ws.Ref)
}

func pluralize(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// confirmScratchDeletion renders the claude_workspace content Remove refused
// to delete, one tree per affected workspace, and asks for an explicit yes.
// Scratch content is durable by contract yet ignored by git, so this is the
// only point where the user sees what removal would silently destroy.
func (s *state) confirmScratchDeletion(scratch *workspace.ErrScratchNotEmpty) (bool, error) {
	for _, c := range scratch.Contents {
		fmt.Fprintf(s.deps.Stderr, "claude_workspace content in %s (never committed; gone for good once removed):\n", c.Ref)
		for _, line := range renderFileTree(c.Files) {
			fmt.Fprintf(s.deps.Stderr, "  %s\n", line)
		}
	}
	ok, err := s.deps.Confirm("Delete this content permanently? [y/N]: ")
	if err != nil {
		return false, &exitErr{code: ExitGeneric, err: err}
	}
	return ok, nil
}

// renderFileTree renders slash-separated relative paths as the indented tree
// `tree` would print, so the confirmation shows structure instead of a wall
// of full paths.
func renderFileTree(paths []string) []string {
	type node struct {
		name     string
		children []*node
	}
	root := &node{}
	index := map[*node]map[string]*node{root: {}}
	for _, p := range paths {
		cur := root
		for seg := range strings.SplitSeq(p, "/") {
			child, ok := index[cur][seg]
			if !ok {
				child = &node{name: seg}
				index[cur][seg] = child
				index[child] = map[string]*node{}
				cur.children = append(cur.children, child)
			}
			cur = child
		}
	}
	var lines []string
	var walk func(n *node, prefix string)
	walk = func(n *node, prefix string) {
		for i, c := range n.children {
			connector, childPrefix := "├── ", prefix+"│   "
			if i == len(n.children)-1 {
				connector, childPrefix = "└── ", prefix+"    "
			}
			lines = append(lines, prefix+connector+c.name)
			walk(c, childPrefix)
		}
	}
	walk(root, "")
	return lines
}

func mapRmError(err error) error {
	var pre *workspace.ErrPrecondition
	var notEmpty *workspace.ErrNotEmpty
	var scratch *workspace.ErrScratchNotEmpty
	var ambiguous *workspace.ErrAmbiguous
	switch {
	case errors.Is(err, workspace.ErrNotFound):
		return &exitErr{code: ExitNotFound, err: err}
	case errors.As(err, &ambiguous):
		return &exitErr{code: ExitUsage, err: err}
	case errors.As(err, &notEmpty):
		return &exitErr{code: ExitPrecondition, err: fmt.Errorf("%w\nrun with --recursive to remove them too", err)}
	case errors.As(err, &scratch):
		return &exitErr{code: ExitPrecondition, err: fmt.Errorf("%w\nmove the content out first, or run with --force to delete it", err)}
	case errors.As(err, &pre):
		return &exitErr{code: ExitPrecondition, err: fmt.Errorf("%w\nrun with --force to override", err)}
	}
	return mapUnclassifiedError(err)
}
