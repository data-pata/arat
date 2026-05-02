# arat

> *Romanian for "plough"; also a name for the killer whale.*

Per-task git-worktree workspaces with Claude Code context. One short binary
that lets you keep many parallel workstreams isolated on disk, jump between
them in one keystroke, and tie each to a ticket without ceremony.

```text
arat new offers-fix --ticket abc-123    # creates feat/abc-123--offers-fix
                                        # with worktrees for every repo + CLAUDE.md
arat go                                 # interactive picker → cd into chosen ws
arat note "shipped a workaround"        # comments on the linked Linear issue
arat rm offers-fix                      # cleans up worktrees + branches
```

`arat` is a generic tool — the org-specific bits (paths, branch prefix,
default repo set, ticket pattern, ticket URL, Linear team) live in a TOML
config, so the same binary can manage workspaces for any
many-repos-in-a-parent-directory layout.

## Status

Working: `ls`, `new`, `rm`, `go` (with picker), `init`, `ticket
create|attach`, `repo add`, `note`, `config init|path`, `version`. Targeted
unit + real-git integration coverage at ~82% overall.

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
| `arat new <name> [--ticket TKT \| --no-ticket] [--repos a,b] [--from-current] [--carry-context] [--code-workspace]` | Create workspace + worktrees + CLAUDE.md. Without `--ticket`/`--no-ticket` and on a tty: opens an interactive ticket flow. |
| `arat rm <name> [--force] [--keep-branches]` (alias `kill`) | Remove workspace; refuses on dirty/unpushed/stashed unless `--force`. |
| `arat go [name]` | Print path to a workspace. With shell wrapper, `cd`s into it. No name → interactive picker. |
| `arat ticket create -t <title> [--team] [--project] [--state] [-d desc] [-l label]` | Create a Linear issue via `linear issue create --no-interactive`. |
| `arat ticket attach <name> <ticket>` | Attach a ticket to a ticketless workspace; renames dirs/branches and updates CLAUDE.md. |
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
| 2 | usage error (bad flags / args) |
| 3 | not found (workspace, ticket, repo) |
| 4 | precondition failed (dirty / unpushed / stashed; pass `--force`) |
| 5 | conflict (already exists) |
| 6 | external tool error (git, linear) |
| 7 | config error |

## Workspace layout

```
<workspaces_dir>/
└── abc-123--offers-fix/
    ├── CLAUDE.md                 # H1, ticket link, branch, repos; user-editable below ## Scope
    ├── claude_workspace/         # gitignored scratch dir; Claude can dump anything here
    ├── core-mono/                # git worktree
    ├── ui-app/                   # git worktree
    └── abc-123--offers-fix.code-workspace   # if generate_code_workspace
```

Branch in each worktree: `me--offers-fix--abc-123` (using the configured
`branch_prefix`).

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
