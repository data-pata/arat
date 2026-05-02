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

`new`, `ls`, `rm`, `go`, `ticket attach|create`, `note`, `init <shell>`,
`config init|path`, `version`. Stable exit codes, `--json` where parsing matters.

## Build phases

1. ✓ skeleton + config + `ls`
2. ✓ `new` (non-interactive) + `rm`
3. ✓ shell integration + `go` (no TUI)
4. ✓ TUI: workspace picker
5. ✓ Linear: `ticket create` + `note`
6. ✓ interactive ticket flow in `new` + `ticket attach`
7. ✓ extras (`--from-current`, `--carry-context`, `--code-workspace`, `auto_repos_glob`)
8. ✓ `repo add` (attach more worktrees to an existing workspace)
9. migrate `~/.claude/skills/ws-*` to call `arat`         ← current

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
