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

Working: `ls`, `new`, `rm`, `go` (with picker), `attach`, `detach`, `init`,
`ticket create`, `repo add`, `note`, `config init|path`, `version`. Targeted
unit + real-git integration coverage.

## Install

Requires Go 1.26+ and `git`. Linear features additionally require the
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

## Daily use

The invocations that cover a normal day. The full flag reference is in [Commands](#commands).

```sh
# start a task
arat new                          # interactive: pick/create a ticket, confirm name and repos
arat new --ticket abc-123         # name derived from the issue title, default repos
arat new quick-probe --no-ticket  # no ticket, explicit name

# move around, see state
arat go                           # recency-sorted picker, cds via the shell wrapper
arat go invoice-pdf               # by bare name (or full ref when ambiguous)
arat ls                           # workspace tree with branches, fast at any size
arat ls --status                  # adds *dirty* *unpushed* *stashes:N* markers

# work inside a task (all infer the workspace from cwd)
arat repo add ui-app              # add another repo's worktree, same feature branch
arat note "found the race, fix is up"   # comment on the linked ticket
arat attach                       # link to a ticket after the fact (interactive)

# projects and sub-issues
arat new q3-billing --project     # top-level container for related tasks
arat new --ticket abc-12          # inside a project: a new issue of that project
arat new --ticket abc-18 --in .   # a sub-issue of the task you are standing in
arat new --ticket abc-20 --in . --from-parent  # same, stacked on the parent's branch

# hand a sub-issue to a parallel Claude agent (see "Working with Claude Code")
arat new --ticket abc-18 --in . --fork-session <session-id>

# finish
arat rm                           # picker, confirms before removing
arat rm invoice-pdf               # refuses on dirty/unpushed worktrees (--force overrides)
arat rm q3-billing --recursive    # a project and everything nested inside it
```

Standing in a task, plain `arat new` creates a sibling task in the same project. Nesting a sub-issue is always explicit with `--in .`.

## Commands

| Command | Purpose |
| --- | --- |
| `arat ls [--flat] [--status] [--json]` | List workspaces and their repos' branches, nested workspaces indented under their parent. Runs no git commands, so it is fast at any repo count; `--status` adds `*dirty* *unpushed* *stashes:N*` markers via git inspection. `--flat` lists every workspace at the top level under its full ref (composable with `--json`). |
| `arat new [name] [--project] [--in REF] [--ticket TKT \| --no-ticket] [--repos a,b] [--from-current] [--carry-context] [--carry-session ID] [--fork-session ID] [--code-workspace]` | Create workspace + worktrees + CLAUDE.md. Without `--ticket`/`--no-ticket` and on a tty: opens an interactive ticket flow. The name is optional when a ticket supplies one: it is derived from the issue title (slugified, ticket id prefixed as usual), shown in a pre-filled prompt on a tty (Enter accepts, Esc cancels; after skipping the ticket you type a name or the command cancels), and derived silently for `--ticket`/`--new-ticket`, so `arat new --ticket abc-12` alone works. `--project` creates a top-level container instead of a leaf; `--in <ref>` places the new workspace inside another one (`--in .` for the workspace at cwd), and `--from-parent` branches off that parent's branches. `--carry-session` moves a Claude Code session jsonl into the new workspace's project dir so `/resume` finds it after `cd`. `--fork-session` copies one there under a fresh session id instead, leaving the original session where it is (see "Working with Claude Code"). |
| `arat rm [ref] [--force] [--keep-branches] [--recursive]` (alias `kill`) | Remove workspace; refuses on dirty/unpushed unless `--force`, and on a workspace that still has others nested inside it unless `--recursive`. No ref → interactive picker. |
| `arat go [ref]` | Print path to a workspace. With shell wrapper, `cd`s into it. No ref → interactive picker. |
| `arat attach [ref] [ticket-or-name] [--new "<title>"] [-d desc]` | Attach a workspace to its Linear counterpart, chosen by the workspace's kind. Task workspace: attach an issue by id (renames dirs/branches, updates CLAUDE.md, migrates `~/.claude/projects/<encoded>` session dirs), or pick/compose one interactively, or `--new` to create it first. Project workspace: link a Linear project or initiative by slug id or name (interactive picker without an argument), or `--new` to create the Linear project in `linear.default_team`. Without a ref: the workspace containing cwd. |
| `arat detach [ref]` | Remove a project workspace's Linear link. A task's ticket cannot be detached (it is baked into dir and branch names). Without a ref: the workspace containing cwd. |
| `arat ticket create -t <title> [--team] [--project] [--state] [-d desc] [-l label]` | Create a Linear issue via `linear issue create --no-interactive`, without touching any workspace. Prints the id for piping. |
| `arat repo add [--workspace NAME] [--base REF] [--recursive] <repo>...` | Add one or more git worktrees to an existing multi-repo workspace, on its existing feature branch. Workspace inferred from cwd if `--workspace` omitted. `--recursive` fans out to every nested workspace, skipping ones that already carry the repo. |
| `arat note [name] <text...>` | Post a comment on the workspace's Linear ticket. Workspace inferred from cwd if name omitted. |
| `arat init <bash\|zsh\|fish>` | Print shell integration. |
| `arat config init [--force] / path` | Write / resolve the config file. |
| `arat version` | Version + git sha. |

`--json` is honoured on `ls`, `new`, `go`, `rm`, `attach`, `detach`,
`repo add`, and `ticket create`. All commands write results to stdout,
operational messages to stderr.

The pre-consolidation forms `arat ticket attach` and `arat project
link|unlink` still work as hidden aliases of `attach`/`detach`.

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | success |
| 1 | generic failure (arat's own: filesystem, TUI, bugs) |
| 2 | usage error (bad flags / args) |
| 3 | not found (workspace, ticket, repo) |
| 4 | precondition failed (dirty / unpushed / non-empty `claude_workspace/`, pass `--force`. Nested workspaces, pass `--recursive`) |
| 5 | conflict (already exists) |
| 6 | external tool error (git, linear) |
| 7 | config error |
| 130 | interrupted (Ctrl-C / SIGTERM; cleanup has already run) |

Guidance for wrappers and scripts:

- `6` is the only code that may be worth retrying: it means git or the
  `linear` CLI failed (network, auth, the tool itself), and nothing else maps
  to it.
- `4` means a safety check refused. Show arat's stderr to the user; never
  auto-escalate to `--force` or `--recursive` from a wrapper.
- `2`, `3`, `5`, and `7` are caller mistakes (bad input, bad ref, taken name,
  bad config): fix the invocation, do not retry.
- `1` is arat's own unclassified failure: treat it as a bug, do not retry.

## Debugging

Set `ARAT_TRACE=1` to log every git / `linear` subprocess to stderr with its
argv, working directory, and duration — everything arat does is a subprocess,
so this shows exactly what ran and what each call cost. Each subprocess is
also bounded by the `command_timeout` config value (default `5m`, `"0"`
disables).

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

## Projects and nesting

Workspaces form a tree that follows Linear's shape:

| Linear | arat |
| --- | --- |
| project | a **project workspace** (`--project`), always at the top level |
| issue in a project | a task workspace nested in a project |
| sub-issue of an issue | a task workspace nested in a task |

A project workspace is a container: other workspaces live inside it as
subdirectories, and it may itself hold worktrees on a long-lived integration
branch. Task workspaces nest too, arbitrarily deep, which is how sub-issues
are represented. Projects are the one thing that never nests, because Linear
has no project inside a project or inside an issue.

```text
arat new q3-billing --project --repos core-mono   # container + its own worktree
cd <workspaces_dir>/q3-billing
arat new invoice-pdf --ticket abc-12              # an issue of the project
cd abc-12--invoice-pdf
arat new fonts --ticket abc-18 --in .             # a sub-issue of it
arat new retry --ticket abc-20 --in q3-billing    # by ref, from anywhere
arat new hotfix --ticket abc-21 --from-parent     # stack on the parent's branch
```

```
<workspaces_dir>/
└── q3-billing/                        # project
    ├── .arat.toml                     # kind = "project"
    ├── CLAUDE.md                      # shared context for everything below
    ├── core-mono/                     # the project's own worktree
    ├── abc-12--invoice-pdf/           # an issue of the project
    │   ├── core-mono/
    │   └── abc-18--fonts/             # a sub-issue of that issue
    │       └── core-mono/
    └── abc-20--retry/
        └── core-mono/
```

Things worth knowing:

- **Workspaces are addressed by ref.** A ref is the slash-joined path from
  `workspaces_dir`, e.g. `q3-billing/abc-12--invoice-pdf/abc-18--fonts`. A
  bare directory name also works when it is unique across the tree, so
  `arat go abc-18--fonts` finds it at any depth. An ambiguous bare name is an
  error listing the full refs rather than a guess — including when one of the
  candidates is a top-level workspace, whose ref is the bare name itself.
  `./<ref>` matches a ref exactly, which is how that top-level workspace is
  addressed while a nested one shares its name.
- **`arat new` inside a task gives you a sibling, not a sub-issue.** Standing
  in a task workspace is the ordinary state of working in one, so cwd
  inference walks up to the containing project. A sub-issue is asked for
  explicitly with `--in .` (the workspace you are in) or `--in <ref>`.
- **Worktrees are opt-in for projects.** `--project` alone creates no
  worktrees, because grouping is the usual reason to make one. Pass `--repos`
  to give the project its own branch.
- **Nesting does not change the base branch.** A nested workspace still
  branches off the latest default branch, the same as a top-level one. Pass
  `--from-parent` to stack on the parent's own branch for every repo the
  parent carries a worktree for. Repos it does not carry keep the normal base
  either way.
- **Removal is explicit.** `arat rm` on a workspace that still contains
  others refuses (exit 4) until you pass `--recursive`. `--force` does not
  substitute for it: `--force` is about discarding *changes*, `--recursive` is
  about discarding whole *workspaces*.
- **`claude_workspace/` content blocks removal.** The scratch dir is where
  notes meant to outlive the code go, yet git ignores it, so `arat rm` is the
  one operation that can destroy it unrecoverably. A non-empty scratch dir
  (beyond the generated `.gitignore`) is listed as a tree and confirmed in a
  terminal, refused (exit 4) outside one, and skipped with `--force`.
- **Linear linking is optional, on both projects and their children.** A
  project workspace works with no Linear link at all. When you do link one, it
  attaches to a Linear **project or initiative** (`arat attach`), not to
  an issue. Initiatives are the only Linear container that nests, and arat has
  no separate workspace kind for them, so a top-level project workspace may
  link to either kind, or to nothing.

Workspaces created before projects existed keep working untouched: a directory
without the `.arat.toml` marker is read as a leaf task workspace.

## Working with Claude Code

arat and Claude Code's parallel-agents features ([agent view](https://code.claude.com/docs/en/agents), [worktree isolation](https://code.claude.com/docs/en/worktrees)) are complementary layers, not alternatives. Claude Code isolates one session per git worktree of a *single* repository, under that repo's `.claude/worktrees/`, on an auto-named branch. arat defines what a *task* is: one directory holding worktrees of every involved repo on a ticket-derived branch, a generated CLAUDE.md with the ticket context, a durable scratch dir, and a place in the project tree. Use arat to shape the workspace, and Claude Code sessions to execute inside it.

The one ground rule: **one concurrent agent per workspace**. Two agents in the same workspace share the same checkouts, index, and branches, and will trample each other. Parallel work belongs in sibling or child workspaces, each with its own worktrees. (Claude Code's own answer to the same problem, one worktree per agent, is single-repo. The arat workspace is the multi-repo equivalent.)

### One agent per sub-issue, in parallel

Working a parent issue in an interactive Claude session, with sub-issues to fan out. No terminal juggling: `!` runs a shell command from inside the session, and `arat new` infers everything else from cwd.

```sh
# 1. inside the parent session, one child workspace per sub-issue
#    (--in . nests; plain `arat new` would create a sibling)
!arat new --ticket abc-101 --in .

# 2. dispatch a background agent into it (`arat go <ref>` prints the path,
#    as does the `arat new` above)
!sh -c 'cd "$(arat go abc-101--pdf-fonts)" && claude --bg --name abc-101 "Take the ticket described in CLAUDE.md to a reviewed PR."'

# 3. monitor all of them, attach/detach at will
claude agents
```

The parent session never stops. Dispatch with `claude --bg` from the workspace directory rather than typing the task into the agent view's dispatcher: agent-view dispatch moves the session into a `.claude/worktrees/` worktree of a single repo, bypassing the multi-repo layout, branch names, and CLAUDE.md the workspace already provides.

### Which session does the child agent get?

- **A fresh session (the default, and usually right).** The child's CLAUDE.md already carries the ticket context, `--carry-context` adds the "Spun off from" header, and a short handoff note in `claude_workspace/` covers the rest. A fresh agent with a tight brief beats one dragging a long parent transcript it will mostly compact away.
- **`--fork-session <id>`: the parent's context, without taking its session.** Copies the transcript into the child workspace under a fresh session id, the same transformation `claude --fork-session` performs, rewriting the embedded ids. The original session keeps running in the parent untouched (the source file is only read, so a live session is safe to fork). Inside the child, `claude --resume <new-id>` continues with the parent's full conversation. Use when the sub-issue genuinely continues reasoning the parent session already did.
- **`--carry-session <id>`: relocate a session.** Moves the jsonl into the child's project dir so `/resume` finds it there, and the old cwd loses it. For "this chat should have lived in a workspace from the start".

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
`cmd/arat/main.go` and `internal/cmd/`. `internal/cmd` composes the other
packages; between the domain packages themselves, imports are limited to
types and error sentinels declared by the producer (`workspace` imports
`git`'s `Inspection`, `tui` reuses `linear`'s and `workspace`'s types) —
never behaviour.

## License

TBD.
