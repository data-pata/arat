# arat

> *Romanian for "plough"; also a name for the killer whale.*

Per-task git-worktree workspaces with Claude Code context. One short binary
that lets you keep many parallel workstreams isolated on disk, jump between
them in one keystroke, and tie each to a ticket without ceremony.

```text
arat new widget-fix --ticket abc-123    # creates feat/abc-123--widget-fix
                                        # with worktrees for every repo + CLAUDE.md
arat go                                 # interactive picker → cd into chosen ws
arat note "shipped a workaround"        # comments on the linked Linear issue
arat rm widget-fix                      # cleans up worktrees + branches
```

`arat` is a generic tool — the org-specific bits (paths, branch prefix,
default repo set, ticket pattern, ticket URL, Linear team) live in a TOML
config, so the same binary can manage workspaces for any
many-repos-in-a-parent-directory layout.

## Status

Working: `ls`, `new`, `rm`, `go` (with picker), `init`, `ticket
create|attach`, `repo add`, `note`, `project link|unlink`, `config init|path`,
`version`. Targeted unit + real-git integration coverage.

## Install

Requires Go 1.24+ and `git`. Linear features additionally require the
[`linear`](https://github.com/schpet/linear-cli) CLI on PATH.

```sh
go install github.com/data-pata/arat/cmd/arat@latest
```

Add the shell wrapper so `arat go` actually `cd`s your shell:

```sh
# zsh
eval "$(arat init zsh)"     # add to ~/.zshrc
# bash
eval "$(arat init bash)"    # add to ~/.bashrc
# fish
arat init fish | source     # in your config.fish
```

## Configure

```sh
arat config init                    # writes ~/.config/arat/config.toml
$EDITOR ~/.config/arat/config.toml
arat config path                    # show resolved path
```

Resolution order: `--config <path>` → `$ARAT_CONFIG` →
`$XDG_CONFIG_HOME/arat/config.toml` → `~/.config/arat/config.toml`.

Minimal config:

```toml
root            = "~/git/<org>"           # where canonical clones live
workspaces_dir  = "~/git/<org>/feat"      # where workspaces are created
branch_prefix   = "me"                    # me--<short>--<ticket>
ticket_pattern  = "^[a-z]+-[0-9]+$"       # which positional args look like tickets
ticket_url      = "https://linear.app/<org>/issue/{TICKET_UPPER}"

default_repos   = ["core-mono", "ui-app", "infra"]
auto_repos_glob = ["core-*", "infra-*"]
generate_code_workspace = false

[linear]
enabled       = true
default_team  = "ABC"
```

## Commands

| Command | Purpose |
| --- | --- |
| `arat ls [--json]` | List workspaces with `*dirty* *unpushed* *stashes:N*` markers. |
| `arat new <name> [--project] [--in REF] [--ticket TKT \| --no-ticket] [--repos a,b] [--from-current] [--carry-context] [--carry-session ID] [--code-workspace]` | Create workspace + worktrees + CLAUDE.md. Without `--ticket`/`--no-ticket` and on a tty: opens an interactive ticket flow. `--project` creates a container instead of a leaf; `--in` places the new workspace inside a project. `--carry-session` moves a Claude Code session jsonl into the new workspace's project dir so `/resume` finds it after `cd`. |
| `arat rm [ref] [--force] [--keep-branches] [--recursive]` (alias `kill`) | Remove workspace; refuses on dirty/unpushed unless `--force`, and on a project that still has nested workspaces unless `--recursive`. No ref → interactive picker. |
| `arat go [ref]` | Print path to a workspace. With shell wrapper, `cd`s into it. No ref → interactive picker. |
| `arat project link <ref> (--project \| --initiative) <slug-or-name>` | Link a project workspace to a Linear project or initiative. |
| `arat project unlink <ref>` | Remove a project workspace's Linear link. |
| `arat ticket create -t <title> [--team] [--project] [--state] [-d desc] [-l label]` | Create a Linear issue via `linear issue create --no-interactive`. |
| `arat ticket attach <name> <ticket>` | Attach a ticket to a ticketless workspace; renames dirs/branches, updates CLAUDE.md, and migrates `~/.claude/projects/<encoded>` session dirs to the new path. |
| `arat repo add [--workspace NAME] [--base REF] <repo>...` | Add one or more git worktrees to an existing multi-repo workspace, on its existing feature branch. Workspace inferred from cwd if `--workspace` omitted. |
| `arat note [name] <text...>` | Post a comment on the workspace's Linear ticket. Workspace inferred from cwd if name omitted. |
| `arat init <bash\|zsh\|fish>` | Print shell integration. |
| `arat config init [--force] / path` | Write / resolve the config file. |
| `arat version` | Version + git sha. |

`--json` is honoured on `ls`, `new`, `ticket create` (where structured output
is useful). All other commands write results to stdout, operational messages
to stderr.

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | success |
| 1 | generic failure (uncategorized) |
| 2 | usage error (bad flags / args) |
| 3 | not found (workspace, ticket, repo) |
| 4 | precondition failed (dirty / unpushed, pass `--force`; non-empty project, pass `--recursive`) |
| 5 | conflict (already exists) |
| 6 | external tool error (git, linear) |
| 7 | config error |

## Workspace layout

```
<workspaces_dir>/
└── abc-123--widget-fix/
    ├── .arat.toml                # workspace marker (kind, Linear link for projects)
    ├── CLAUDE.md                 # H1, ticket link, branch, repos; user-editable below ## Scope
    ├── claude_workspace/         # gitignored scratch dir; Claude can dump anything here
    ├── core-mono/                # git worktree
    ├── ui-app/                   # git worktree
    └── abc-123--widget-fix.code-workspace   # if generate_code_workspace
```

Branch in each worktree: `me--widget-fix--abc-123` (using the configured
`branch_prefix`).

## Projects

A **project workspace** is a container: other workspaces live inside it as
subdirectories, and it may itself hold worktrees on a long-lived integration
branch. Projects nest, so a project can contain further projects.

```text
arat new q3-billing --project --repos core-mono   # container + its own worktree
cd <workspaces_dir>/q3-billing
arat new invoice-pdf --ticket abc-12              # nested here, inferred from cwd
arat new dunning --project                        # a sub-project
arat new retry --ticket abc-20 --in q3-billing/dunning
arat new hotfix --ticket abc-21 --from-project    # stack on the project's branch
```

```
<workspaces_dir>/
└── q3-billing/                        # project
    ├── .arat.toml                     # kind = "project"
    ├── CLAUDE.md                      # shared context for everything below
    ├── core-mono/                     # the project's own worktree
    ├── abc-12--invoice-pdf/           # nested workspace
    │   └── core-mono/
    └── dunning/                       # sub-project
        └── abc-20--retry/
            └── core-mono/
```

Things worth knowing:

- **Workspaces are addressed by ref.** A ref is the slash-joined path from
  `workspaces_dir`, e.g. `q3-billing/dunning/abc-20--retry`. A bare directory
  name also works when it is unique across the tree, so `arat go abc-20--retry`
  finds it at any depth. An ambiguous bare name is an error listing the full
  refs rather than a guess.
- **Worktrees are opt-in for projects.** `--project` alone creates no
  worktrees, because grouping is the usual reason to make one. Pass `--repos`
  to give the project its own branch.
- **Nesting does not change the base branch.** A workspace created inside a
  project still branches off the latest default branch, the same as a
  top-level one. Pass `--from-project` to stack on the project's own branch
  for every repo it carries a worktree for. Repos the project does not carry
  keep the normal base either way.
- **Removal is explicit.** `arat rm` on a project that still contains
  workspaces refuses (exit 4) until you pass `--recursive`. `--force` does not
  substitute for it: `--force` is about discarding *changes*, `--recursive` is
  about discarding whole *workspaces*.
- **Linear linking is optional, on both projects and their children.** A
  project workspace works with no Linear link at all. When you do link one, it
  attaches to a Linear **project or initiative** (`arat project link`), not to
  an issue. Note that Linear itself does not nest projects — only initiatives
  have parents — so arat does not require your nesting to mirror Linear's.
  A nested arat project may link to either kind, or to nothing.

Workspaces created before projects existed keep working untouched: a directory
without the `.arat.toml` marker is read as a leaf task workspace.

## Phase 7 extras

- **`--from-current`**: when run from inside a workspace, branches new
  worktrees off the parent's feature branches per-repo (instead of
  `origin/HEAD`). Use to spin a sibling task off a WIP branch.
- **`--carry-context`**: seeds the new CLAUDE.md with a `Spun off from
  <parent>` line including the parent's ticket link if present.
- **`--code-workspace`**: writes a `<name>.code-workspace` JSON file
  alongside the workspace dir (multi-root project for VS Code). Also
  enabled globally via `generate_code_workspace = true` in config.

## Architecture

```
cmd/arat/main.go            entry point; wires real impls
internal/
  cmd/                      cobra subcommand definitions (thin)
  config/                   TOML loader, defaults, validation
  workspace/                domain: Service{Git,FS,Clock} — the core
  git/                      thin git CLI wrapper
  linear/                   shell-out to `linear` CLI (issue / comment / api)
  tui/                      bubbletea pickers (workspace, action chooser, issue)
  output/                   --json gating, record printing
  shell/                    bash/zsh/fish init script templates
```

Convention: packages declare their deps as interfaces, composition lives in
`cmd/arat/main.go` and `internal/cmd/`. No package imports a sibling domain
package.

## License

TBD.
