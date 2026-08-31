# devtask v1 Design

## Scope

devtask v1 supports macOS and Linux. It is a thin, local orchestration layer for one Task spanning multiple existing Git repositories. It does not clone repositories, modify Main Checkouts, implement an agent, or perform remote hosting operations.

The Go module is `github.com/Leonz3n/devtask`. The implementation targets the current stable Go toolchain and uses Cobra, `yaml.v3`, and `golang.org/x/sys/unix` without a shell command layer.

## Commands

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

Core commands never prompt. Dangerous capabilities require explicit, narrow flags. `--forget` only detaches metadata and derived workspace links when both the expected Task Worktree path and Git worktree record are absent; it never removes a worktree or branch.

Task names, Repository Aliases, and Repository Group names match `[A-Za-z0-9][A-Za-z0-9._-]*`, excluding `.` and `..`. Uniqueness is checked case-insensitively while preserving spelling. Repository Aliases also cannot collide with reserved Task Context File names. Rendered Git branch names receive Git's own branch-name validation.

## Storage

Paths follow XDG Base Directory rules. Relative XDG environment values are ignored.

```text
$XDG_CONFIG_HOME/devtask/config.yaml
$XDG_DATA_HOME/devtask/tasks/<task>.yaml
$XDG_DATA_HOME/devtask/workspaces/<task>/
```

Defaults are `~/.config` and `~/.local/share`. State directories use mode `0700` and newly created metadata files use `0600`. `init` is explicit and idempotent; other commands fail with a concise initialization instruction when state is absent.

Configuration and Task metadata include `schema_version: 1`. Unknown fields or schema versions are errors. Writes use a temporary file in the destination directory, file sync, atomic rename, and directory sync. Advisory file locks are held through each mutation: one config lock, one lock per Task, and one lock per canonical Registered Repository participating in a Git mutation. Multi-repository commands acquire repository locks in canonical-path order to avoid deadlock. A busy lock fails clearly instead of waiting indefinitely. Locks are never bypassed by `--force`.

Configuration is human-editable:

```yaml
schema_version: 1

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
  invoice-service:
    path: /Users/me/Workspace/projects/invoice-service
    base_branch: main
    remote: origin
    fetch: true
    shared_paths:
      - .env
      - config/local.yaml
      - certs

groups:
  billing:
    - invoice-service
    - billing-service
```

`shared_paths` is deliberately named for both files and directories. Optional repository values inherit global defaults. CLI values override repository values, which override global values. Agent `command` is one executable name or path, not a shell expression.

`branch_pattern` is parsed as a restricted Go text template with `.Task` as its only value and no custom functions. It renders once during `new`; both template execution and Git branch-name validation must succeed before metadata is written.

`repo add` resolves an input directory to the repository's Main Checkout, including when the input is inside a linked worktree. Bare repositories are rejected. Repeating the same alias and canonical path is idempotent; changing the path requires `--update`. YAML node editing preserves unrelated comments and ordering. Registry changes never rewrite existing Repository Attachments.

## Task Metadata

Each Task stores its rendered Task Branch Name, creation time in UTC RFC 3339 form, state (`ready` or `incomplete`), generated-file ownership data, and an ordered sequence of Repository Attachments. Each attachment snapshots:

- Repository Alias and canonical Main Checkout path;
- canonical Task Worktree path;
- Task Branch Name;
- requested base branch and resolved ref/commit when a branch is created;
- whether the branch already existed;
- managed Shared Local Path links;
- attachment state and any residual error.

An incomplete composite operation also records its operation kind and residual objects so `status` can give manual recovery instructions. Task Workspace links and generated AGENTS content are derived views, not the source of truth.

Repository Groups expand only during `new`. Expansion preserves group order, applies exclusions, appends explicit additions in command-line order, and de-duplicates aliases. Unknown aliases and exclusions that do not occur in the selected group are errors. Later group edits do not change an existing Task.

## Git Lifecycle

### Preparation

The Task Worktree path is exactly `<main-checkout>/.worktrees/<task>`. Canonical containment checks resolve symlinks and account for platform path aliases such as `/tmp` and `/private/tmp`. The target must remain beneath the canonical `.worktrees` directory.

Git does not automatically ignore nested linked worktrees. Before the first add, devtask checks effective ignore behavior and, when necessary, append-only adds `/.worktrees/` to the common Git `info/exclude`. It never rewrites `.gitignore` or existing exclude content.

### Base Resolution

The base option is a branch name, not an arbitrary commit expression. Resolution is:

1. CLI base, repository base, global base, then `main`.
2. If fetch is enabled and the configured remote exists, fetch that remote; failure aborts.
3. Prefer the fetched remote-tracking branch.
4. Otherwise use a matching local branch.
5. With no configured remote present, skip fetch and use the local branch.
6. Fail when neither exists.

`--no-fetch` permits current references; `--fetch` overrides disabled configuration.

### Creation Matrix

- Missing Task Branch: create it from the resolved base with `git worktree add --no-track -b`.
- Existing, unassigned Task Branch: attach it at its current tip and report that the base option was not applied.
- Branch already assigned to the expected path and matching metadata: report idempotent success.
- Branch assigned elsewhere, including a prunable record: stop and identify the owner; never prune automatically.
- Destination exists without the expected Git record and Task ownership: stop; never adopt automatically.

Multi-repository creation performs full preflight, then applies changes in attachment order. On failure it rolls back only objects created by that invocation. A failed rollback persists `incomplete` state and residual facts. Metadata becomes `ready` only after the Git worktree, managed links, Task Workspace link, and generated AGENTS section are established.

### Status

Git status uses NUL-delimited porcelain output and retains independent `modified`, `untracked`, and `conflicted` flags. Human output displays combinations. `missing`, `incomplete`, and `unknown` describe structural or inspection failures and always block ordinary removal.

Ignored content is inspected separately during removal. Exact devtask-managed Shared Local Path symlinks are allowed; unknown ignored content is protected like other local changes.

### Removal

All attachments are preflighted before destructive work begins. The target must be the recorded linked worktree, beneath `.worktrees`, on the expected branch, and not the Main Checkout. `--force` only permits removal of dirty, untracked, or unknown ignored content.

Branches are retained by default. With `--delete-branch`, devtask fetches and resolves the current base and verifies the Task Branch is its ancestor before deletion. An unmerged branch requires both `--delete-branch` and `--force`; `--no-fetch` can explicitly select current local references for an offline removal. Branch deletion is reported separately from worktree removal. `--forget` cannot be combined with branch deletion.

Each successful removal is persisted immediately because a later removal cannot be rolled back. Missing external state is diagnosed; `--forget` is the explicit metadata-only recovery when both path and Git record are absent.

## Shared Local Paths

Configured paths must be relative, remain inside the canonical Main Checkout, and may name files, directories, or symlinks. The path must be untracked and effectively ignored in both the Main Checkout and destination Task Worktree; devtask does not create a link that would make Git status dirty. A missing source, non-ignored source/destination, or existing destination produces a warning and is skipped without overwriting anything. A created link is relative, calculated with `filepath.Rel`, and its source/destination identity is recorded. A configured entry may itself be a symlink whose ultimate target is outside the repository; devtask links to the entry and does not take ownership of that external target.

Before removal, a managed link is exempted from ignored-content protection only when `Lstat` confirms it remains the expected symlink. A replaced file, directory, or redirected link is user data and blocks ordinary removal.

## Task Workspace

`new` creates `TASK.md` and `AGENTS.md`. `TASK.md` is never regenerated. AGENTS content is maintained only between explicit generated markers; content outside the markers is preserved. Repository links are relative symlinks named by Repository Alias and follow attachment order in generated documentation.

Workspace collisions are reused only when ownership by the same Task can be proven. Missing derived links or generated content may be repaired automatically; Git objects are never adopted through this repair.

Normal Task removal deletes generated files only when they still match their generated form. New or edited Task Context Files block removal unless `--force` is explicit.

## Agent Launchers

All launchers connect the current terminal directly to the child process and return its exit code. Executables are resolved without a shell.

- Pi uses the Task Workspace as its process cwd.
- Claude uses the first Task Worktree as cwd, adding the remaining Task Worktrees and Task Workspace through repeated `--add-dir=<path>` arguments. The equals form binds exactly one directory per occurrence and cannot consume a later prompt.
- Codex uses `-C` for the first Task Worktree, adding the remaining Task Worktrees and Task Workspace through repeated `--add-dir`.
- With no Repository Attachments, all three use the Task Workspace as cwd.

The first Task Worktree means attachment creation order, not map or alphabetical order. User arguments are forwarded unchanged except that workspace-conflicting arguments are rejected: Claude may not request its own worktree, and Codex may not override `-C`. Additional user-supplied directories are allowed.

An Agent Launcher holds shared Task and Registered Repository locks for the lifetime of the child process. Mutating devtask commands require exclusive locks and therefore cannot remove or reshape that Task through devtask while the launched agent is running. These advisory locks cannot protect an agent or Git command started outside devtask.

## Output And Errors

Human output is concise and may abbreviate home-directory paths. JSON always uses absolute paths, RFC 3339 timestamps, explicit status flags, and stable field names. Data goes to stdout and diagnostics to stderr.

For devtask-owned behavior, exit code `0` means success, `2` means CLI usage or validation failure, and `1` means an operational Git, filesystem, or metadata failure. A successfully started Agent Launcher returns the child process's exact exit code. Errors identify the Task and repository, the failed operation, relevant sanitized command arguments, observed state, and recovery action; they never stop at an exit status alone.

## Implementation Shape

```text
cmd/devtask/
internal/config/
internal/git/
internal/lock/
internal/repo/
internal/task/
internal/workspace/
internal/runner/
internal/cli/
```

Git command execution and interactive child execution use separate runner paths. Cobra handlers translate arguments and render results; they do not implement Git or filesystem workflows.

Implementation proceeds through the four requested phases. Each phase adds unit tests plus real temporary-repository integration tests, then runs the full test suite, race-enabled tests where applicable, `go vet`, and formatting checks before the next phase.
