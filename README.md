# devtask

**English** | [简体中文](./README.zh-CN.md)

`devtask` coordinates one local development **Task** across several existing Git repositories. It creates an independent **Task Worktree** in each **Registered Repository**, joins them in a non-Git **Task Workspace**, and launches Pi, Claude Code, or Codex with access to the same files.

devtask v1 supports macOS and Linux. It never clones repositories, changes a **Main Checkout**, commits, pushes, merges, prunes worktrees, or creates pull requests.

## Install

Prerequisites:

- Go 1.26 or newer
- Git available as `git`
- macOS or Linux

Install the latest version from source:

```sh
go install github.com/Leonz3n/devtask/cmd/devtask@latest
devtask --help
```

To build the current checkout instead:

```sh
go build -o ./bin/devtask ./cmd/devtask
```

## Quick start

Initialize explicit local state, then register existing repositories under stable **Repository Aliases**:

```sh
devtask init
devtask repo add api ~/src/api-service
devtask repo add web ~/src/web-app
devtask repo list
```

`repo add` accepts the Main Checkout, a directory inside it, or a linked worktree. It always resolves and stores the canonical Main Checkout. Repeating the same alias and path is safe; changing an alias to a different repository requires `--update`.

Create an empty Task and attach repositories later:

```sh
devtask new billing-rollout
devtask add billing-rollout api web
```

The default **Task Branch Name** is `feat/<task>`. Override it when creating the Task:

```sh
devtask new billing-rollout --branch feature/BILL-123
```

The Task Branch Name is frozen in Task metadata. Later configuration changes do not rename existing work.

Inspect Tasks and launch an agent. Arguments after `--` are passed directly to the selected executable:

```sh
devtask list
devtask status billing-rollout
devtask status billing-rollout --json

devtask pi billing-rollout -- --model claude-sonnet-4
devtask claude billing-rollout -- "Review the whole Task"
devtask codex billing-rollout -- "Implement the Task"
```

When work is finished, remove the Task Worktrees and Task Workspace. Task Branches are retained by default:

```sh
devtask remove billing-rollout
```

## Configuration

`devtask init` creates a strict, versioned YAML configuration at:

- `$XDG_CONFIG_HOME/devtask/config.yaml` when `XDG_CONFIG_HOME` is absolute
- `~/.config/devtask/config.yaml` otherwise

Task metadata and Task Workspaces use `$XDG_DATA_HOME/devtask` when `XDG_DATA_HOME` is absolute, or `~/.local/share/devtask` otherwise. Other commands never initialize state implicitly.

Set the top-level `task_workspace_root` when Task Workspaces should be directly accessible in Finder or an agent that accepts only one directory. The value must be an absolute path; `~` is not expanded in YAML. Configure it before creating Tasks because devtask does not move existing Task Workspaces.

A complete configuration can look like this:

```yaml
schema_version: 1

# Optional; must be absolute. Defaults to devtask/workspaces under the XDG data directory.
task_workspace_root: /Users/me/DevTasks

defaults:
  base_branch: main
  branch_pattern: "feat/{{.Task}}"
  remote: origin
  fetch: true

agents:
  pi:
    command: pi
  claude:
    command: claude
  codex:
    command: codex

repositories:
  api:
    path: /Users/me/src/api-service
    base_branch: main
    remote: origin
    fetch: true
    shared_paths:
      - .env
      - config/local.yaml
      - certs
  web:
    path: /Users/me/src/web-app

groups:
  billing:
    - api
    - web
```

Unknown fields and unsupported schema versions are errors. Agent `command` values are one executable name or absolute path, never shell expressions.

For the base branch and fetch behavior, command-line options override Registered Repository settings, which override global defaults. The remote is selected by Registered Repository settings and then global defaults; v1 has no command-line remote override. If no value is configured, the effective defaults are `main`, `origin`, and fetch enabled.

## Repository Groups

A **Repository Group** is an ordered selection expanded only when a Task is created:

```sh
devtask new billing-rollout --group billing
devtask new billing-rollout --group billing --exclude web --add worker
```

Exclusions are applied first, explicit additions are appended in command-line order, and duplicate aliases are removed. Unknown aliases and ineffective exclusions fail before mutation. Editing a Repository Group later does not change an existing Task's Repository Attachments.

## Git and offline behavior

For each new Repository Attachment, devtask creates `<main-checkout>/.worktrees/<task>` and a Task Branch with upstream tracking disabled. The Main Checkout's branch and files are left unchanged. If the Task Branch already exists and is not assigned to another worktree, devtask attaches it at its current tip and does not apply the requested Base Ref.

With fetch enabled and the configured remote present, devtask fetches that remote, prefers its remote-tracking base branch, then falls back to a matching local branch. A fetch failure stops the operation instead of silently using stale state. Repositories without the configured remote use the local base branch.

Use `--no-fetch` only when current local references are intentionally acceptable, such as while offline:

```sh
devtask add billing-rollout api web --no-fetch
devtask new billing-rollout --group billing --no-fetch
devtask remove billing-rollout --delete-branch --no-fetch
```

`--fetch` explicitly enables fetching when configuration disables it. `--fetch` and `--no-fetch` are mutually exclusive.

devtask never prunes a stale Git worktree record, steals a branch assigned elsewhere, adopts an unknown directory, resets a branch, or repairs Git objects automatically.

## Shared Local Paths

`shared_paths` entries are repository-relative, unversioned files, directories, or symlink entries owned by the Main Checkout. When a Repository Attachment is created, devtask projects each valid entry into the Task Worktree as a relative symlink and records its identity.

A Shared Local Path is skipped with a warning when it is missing, tracked, not effectively ignored in both locations, escapes the Main Checkout, or collides with an existing Task Worktree entry. devtask never overwrites the destination. If a managed link is later replaced or redirected, it becomes protected local data and blocks ordinary removal.

## Task Workspace and Agent Launchers

Each Task Workspace contains:

- `TASK.md`, created once and never regenerated
- `AGENTS.md`, with only the marked generated section maintained by devtask
- one relative symlink per Repository Attachment, in attachment order

Text outside the generated markers in `AGENTS.md` is preserved. Task Workspace paths are printed by `devtask status`.

Agent Launchers execute the configured program directly without a shell and connect it to the current terminal:

- Pi starts in the Task Workspace.
- Claude Code starts in the first Task Worktree and receives the remaining Task Worktrees plus the Task Workspace through `--add-dir=<path>`.
- Codex receives the first Task Worktree through `-C` and the remaining Task Worktrees plus the Task Workspace through repeated `--add-dir` options.
- For an empty Task, all three start in the Task Workspace.

The first Task Worktree is determined by Repository Attachment creation order. Launcher arguments must follow `--`. Claude's worktree options and Codex's working-root options are rejected because devtask owns that layout; extra `--add-dir` options are allowed. A successfully started launcher returns the child's exact exit code and holds shared Task and Registered Repository locks until the child exits.

## Status and automation

```sh
devtask repo list --json
devtask list --json
devtask status billing-rollout --json
```

Human output is concise and may abbreviate paths under the home directory with `~`. JSON uses stable snake-case fields, absolute paths, UTC RFC 3339 timestamps, and explicit independent status flags. `status` can report `modified`, `staged`, `untracked`, and `conflicted` together; `missing`, `unknown`, and `incomplete` describe structural or inspection problems.

Data is written to stdout and diagnostics to stderr. devtask-owned exit codes are:

| Code | Meaning |
| ---: | --- |
| `0` | success |
| `1` | operational Git, filesystem, lock, or metadata failure |
| `2` | CLI usage, invalid configuration, or unsafe/invalid request |

Once an Agent Launcher starts, its child exit code is returned unchanged. Startup diagnostics identify the executable but do not repeat forwarded agent arguments.

## Safe removal

Remove one Repository Attachment or the whole Task:

```sh
devtask remove-repo billing-rollout web
devtask remove billing-rollout
```

Before removal, devtask verifies the exact Main Checkout, Task Worktree path, Git worktree record, Task Branch Name, Task Workspace link, Git status, ignored content, and Task Context Files. Modified, staged, untracked, conflicted, or unknown ignored content blocks ordinary removal. Edited or new Task Context Files also block full Task removal.

Dangerous capabilities are explicit and never prompted for:

- `--force` permits deletion of protected Task Worktree content and, for full Task removal, protected Task Context Files. It never bypasses path containment, Main Checkout protection, object identity, or locks.
- `--delete-branch` requests Task Branch deletion. The branch must be merged into the current Base Ref; deleting an unmerged branch additionally requires `--force`.
- `--forget` removes one Repository Attachment from metadata only after both its Task Worktree path and Git worktree record are already absent. It cannot be combined with `--delete-branch`.

Examples:

```sh
# Keep Task Branches (default).
devtask remove billing-rollout

# Delete Task Branches only after verifying they are merged.
devtask remove billing-rollout --delete-branch

# Explicitly authorize both local-data loss and unmerged branch deletion.
devtask remove billing-rollout --delete-branch --force
```

## Incomplete state and recovery

Operations spanning repositories use preflight plus best-effort compensation; independent Git repositories cannot form one atomic transaction. If compensation or an irreversible removal step fails, devtask records the Task or Repository Attachment as `incomplete` with the last error, residual objects, and recovery instructions.

Start with:

```sh
devtask status <task>
devtask status <task> --json
```

Inspect every reported path and Git worktree record before changing anything. Do not rerun `add`, launch an agent, or force a normal removal while the Task remains incomplete. Follow the reported recovery instructions:

- Restore an expected object when it still owns work that must be kept.
- Remove only a residual object whose identity and contents you have verified.
- After external cleanup has removed both a Task Worktree path and its Git record, run `devtask remove-repo <task> <alias> --forget` to reconcile metadata and the Task Workspace projection.
- If an interrupted grouped creation left only Task Workspace or metadata residuals and no recoverable Repository Attachment, back up any useful Task Context Files, verify the exact residual paths printed by `status`, and remove those residuals manually before recreating the Task.

There is no general automatic repair command in v1. This is intentional: when Git ownership cannot be proved, devtask reports facts rather than adopting or deleting unknown state.

## Command reference

```text
devtask init
devtask repo add [--update] <alias> <path>
devtask repo list [--json]
devtask new <task> [--branch <name>] [--group <group>] [--exclude <alias>] [--add <alias>] [--base <branch>] [--fetch|--no-fetch]
devtask add <task> <alias>... [--base <branch>] [--fetch|--no-fetch]
devtask list [--json]
devtask status <task> [--json]
devtask remove-repo <task> <alias> [--force] [--delete-branch] [--forget] [--fetch|--no-fetch]
devtask remove <task> [--force] [--delete-branch] [--fetch|--no-fetch]
devtask pi <task> [-- <args>...]
devtask claude <task> [-- <args>...]
devtask codex <task> [-- <args>...]
```

Task names, Repository Aliases, and Repository Group names use `[A-Za-z0-9][A-Za-z0-9._-]*`; `.` and `..` are invalid, and conflicts are checked case-insensitively. Repository Aliases also cannot collide with Task Context File names such as `TASK.md` or `AGENTS.md`.
