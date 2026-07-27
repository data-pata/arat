# arat

Generic Go CLI: per-task git-worktree workspaces with Claude context. Decoupled from
any specific company/team — org-specific values (paths, branch prefix, default repo
set, ticket regex, ticket URL template, Linear team) are all config values.

## Layout

```
cmd/arat/main.go            entry point; wires real impls, calls cmd.Execute()
internal/
  cmd/                      cobra subcommand definitions (thin)
  config/                   TOML loader, defaults, validation
  workspace/                domain: Workspace types + Service{Git,FS,Clock}
  git/                      thin git CLI wrapper
  linear/                   shell-out to `linear` CLI
  tui/                      bubbletea pickers (workspace, ticket, ticket-create)
  output/                   --json gating, record printing
  shell/                    bash/zsh/fish init script templates
```

Convention: packages declare their deps as interfaces, composition lives in
`cmd/arat/main.go` and `internal/cmd/`. No package imports a sibling domain package.

## Commands (target)

`new`, `ls`, `rm`, `go`, `attach`, `detach`, `ticket create`, `note`,
`repo add`, `init <shell>`, `config init|path`, `version`. `attach`/`detach`
are kind-aware (task ↔ issue, project ↔ Linear project/initiative); the older
`ticket attach` and `project link|unlink` remain as hidden aliases. Stable
exit codes, `--json` where parsing matters.

## Workspace tree

Workspaces nest, and the tree mirrors Linear's containment rules. A workspace
is either `kind = "task"` or `kind = "project"`. Both may hold worktrees and
both may hold child workspaces, so the kinds differ only in what they mean and
where they may sit:

| Linear | arat |
| --- | --- |
| project | `kind = "project"`, top level only |
| issue in a project | `kind = "task"` nested in a project |
| sub-issue of an issue | `kind = "task"` nested in a task |

Nesting is physical: a child workspace is a subdirectory of its parent.
`validateNew` enforces the one structural rule, that a project may not have a
parent, because Linear has no project inside a project or inside an issue and
a tree containing one could never round-trip to a linked Linear tree.

Each workspace arat creates carries a `.arat.toml` marker at its root. The
marker is what disambiguates a workspace's subdirectories: one that carries it
is a child workspace, one git calls a worktree is a repo, anything else is
ignored. `Service.hydrateContents` runs that same classification for both
kinds. A directory with no marker reads as a task workspace, so workspaces
predating projects keep working with no migration.

`arat new` infers its parent from cwd via `Service.ProjectAt`, which walks up
to the nearest *project*, past any number of tasks. Standing in a task is the
ordinary state of working in one, so plain `arat new` there means a sibling.
A sub-issue is requested explicitly with `--in .` or `--in <ref>`.

Workspaces are addressed by **ref** (`Workspace.Ref`): the slash-joined path
from `workspaces_dir`. `Service.Get` accepts a full ref or a bare name that is
unique across the tree, returning `*ErrAmbiguous` rather than guessing.
`Service.List` returns the top level with `Children` populated recursively;
`workspace.Flatten` gives every workspace regardless of depth.

## Build phases

1. ✓ skeleton + config + `ls`
2. ✓ `new` (non-interactive) + `rm`
3. ✓ shell integration + `go` (no TUI)
4. ✓ TUI: workspace picker
5. ✓ Linear: `ticket create` + `note`
6. ✓ interactive ticket flow in `new` + `ticket attach`
7. ✓ extras (`--from-current`, `--carry-context`, `--code-workspace`, `auto_repos_glob`)
8. ✓ `repo add` (attach more worktrees to an existing workspace)
9. ✓ project workspaces (nested tree, `--project`/`--in`, `project link`)
10. migrate the shell/skill wrappers to call `arat`       ← current

## Conventions

- `fmt.Fprintf(os.Stderr, ...)` for operational output, stdout for results / JSON.
- Table-driven tests with `t.Helper()`, `t.TempDir()`.
- Real git for `internal/git` tests; fake `Runner` for `internal/linear` tests.
- Cobra `--help` is the primary docs surface.

## Exit codes

- `0` success
- `2` usage error
- `3` not found
- `4` precondition failed (dirty / unpushed / stash; `--force` overrides)
- `5` conflict (already exists)
- `6` external tool error (git, linear)
- `7` config error
