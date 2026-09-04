# devtask

[English](./README.md) | **简体中文**

`devtask` 用于协调一个跨多个现有 Git 仓库的本地开发 **Task（任务）**。它会在每个 **Registered Repository（已注册仓库）** 中创建独立的 **Task Worktree（任务工作树）**，通过一个非 Git 的 **Task Workspace（任务工作区）** 将它们组织在一起，并让 Pi、Claude Code 或 Codex 访问同一批文件。

devtask v1 支持 macOS 和 Linux。它不会 clone 仓库，不会修改 **Main Checkout（主检出目录）**，也不会自动 commit、push、merge、prune worktree 或创建 Pull Request。

## 安装

前置条件：

- Go 1.26 或更高版本
- 系统中可通过 `git` 命令使用 Git
- macOS 或 Linux

从源码安装最新版本：

```sh
go install github.com/Leonz3n/devtask/cmd/devtask@latest
devtask --help
```

也可以构建当前 checkout：

```sh
go build -o ./bin/devtask ./cmd/devtask
```

## 快速开始

先显式初始化本地状态，再使用稳定的 **Repository Alias（仓库别名）** 注册已有仓库：

```sh
devtask init
devtask repo add api ~/src/api-service
devtask repo add web ~/src/web-app
devtask repo list
```

`repo add` 接受 Main Checkout、其中任意子目录或 linked worktree。它始终解析并保存规范化后的 Main Checkout。重复注册同一 alias 和路径是安全的；如果要把 alias 改到另一个仓库，必须显式使用 `--update`。

创建一个空 Task，之后再添加仓库：

```sh
devtask new billing-rollout
devtask add billing-rollout api web
```

默认的 **Task Branch Name（任务分支名）** 是 `feat/<task>`。创建 Task 时可以覆盖它：

```sh
devtask new billing-rollout --branch feature/BILL-123
```

Task Branch Name 会固化在 Task metadata 中。之后修改配置不会重命名已有工作。

查看 Task 并启动 Agent Launcher。`--` 后面的参数会直接传递给所选程序：

```sh
devtask list
devtask status billing-rollout
devtask status billing-rollout --json

devtask pi billing-rollout -- --model claude-sonnet-4
devtask claude billing-rollout -- "Review the whole Task"
devtask codex billing-rollout -- "Implement the Task"
```

工作完成后，删除 Task Worktrees 和 Task Workspace。默认保留 Task Branch Names：

```sh
devtask remove billing-rollout
```

## 配置

`devtask init` 会在以下位置创建严格校验、带版本号的 YAML 配置：

- 当 `XDG_CONFIG_HOME` 是绝对路径时：`$XDG_CONFIG_HOME/devtask/config.yaml`
- 否则：`~/.config/devtask/config.yaml`

Task metadata 和 Task Workspaces 在 `XDG_DATA_HOME` 为绝对路径时使用 `$XDG_DATA_HOME/devtask`，否则使用 `~/.local/share/devtask`。其他命令不会隐式初始化状态。

如果希望在 Finder 或只支持单目录的 Agent 中直接打开 Task Workspace，可以设置顶层 `task_workspace_root`。该值必须是绝对路径；YAML 中的 `~` 不会展开。请在创建 Task 前完成设置；devtask 不会搬迁已经存在的 Task Workspace。

完整配置示例：

```yaml
schema_version: 1

# 可选；必须是绝对路径。未配置时使用 XDG data 目录下的 devtask/workspaces。
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

未知字段和不支持的 schema version 会被视为错误。Agent `command` 只能是一个可执行文件名或绝对路径，不能是 shell 表达式。

对于 base branch 和 fetch 行为，命令行选项优先于 Registered Repository 设置，后者又优先于全局默认值。remote 先读取 Registered Repository 设置，再读取全局默认值；v1 不提供命令行 remote override。未配置时，有效默认值分别是 `main`、`origin` 和启用 fetch。

## Repository Groups（仓库组）

**Repository Group** 是一个有序仓库集合，仅在创建 Task 时展开：

```sh
devtask new billing-rollout --group billing
devtask new billing-rollout --group billing --exclude web --add worker
```

程序会先应用 exclusions，再按命令行顺序追加显式 additions，并去除重复 alias。未知 alias 或无效 exclusion 会在任何修改发生前失败。之后编辑 Repository Group 不会改变已有 Task 的 Repository Attachments。

## Git 与离线行为

对于每个新的 Repository Attachment，devtask 会创建 `<main-checkout>/.worktrees/<task>`，并创建一个不设置 upstream tracking 的 Task Branch Name。Main Checkout 的分支和文件保持不变。如果 Task Branch Name 已存在且没有被其他 worktree 使用，devtask 会从其当前 tip 创建 Task Worktree，不会应用请求的 **Base Ref**。

启用 fetch 且配置的 remote 存在时，devtask 会 fetch 该 remote，优先使用 remote-tracking base branch，再回退到匹配的本地分支。fetch 失败会中止操作，而不是静默使用过期状态。没有配置 remote 的仓库使用本地 base branch。

只有在明确接受当前本地引用时才使用 `--no-fetch`，例如离线工作：

```sh
devtask add billing-rollout api web --no-fetch
devtask new billing-rollout --group billing --no-fetch
devtask remove billing-rollout --delete-branch --no-fetch
```

配置禁用 fetch 时，`--fetch` 可以显式启用它。`--fetch` 和 `--no-fetch` 互斥。

devtask 不会自动 prune 过期的 Git worktree record，不会抢占已被其他 worktree 使用的分支，不会接管未知目录，也不会自动 reset 分支或修复 Git 对象。

## Shared Local Paths（共享本地路径）

`shared_paths` 条目是 Main Checkout 所拥有的、相对于仓库的未版本化文件、目录或 symlink entry。创建 Repository Attachment 时，devtask 会将每个有效条目作为相对 symlink 投影到 Task Worktree，并记录其 identity。

如果 Shared Local Path 不存在、已被跟踪、在源和目标中没有被有效 ignore、逃逸出 Main Checkout，或与 Task Worktree 中的现有条目冲突，devtask 会发出 warning 并跳过。它不会覆盖目标。如果 managed link 之后被替换或重定向，该位置会成为受保护的本地数据，阻止普通删除。

## Task Workspace 与 Agent Launchers

每个 Task Workspace 包含：

- `TASK.md`：只创建一次，不会重新生成
- `AGENTS.md`：devtask 只维护带标记的 generated section
- 按 Attachment 顺序排列的 Repository Attachment 相对 symlink

`AGENTS.md` generated markers 之外的文本会保留。`devtask status` 会显示 Task Workspace 路径。

Agent Launchers 不经过 shell，直接执行配置的程序并连接当前终端：

- Pi 从 Task Workspace 启动。
- Claude Code 从第一个 Task Worktree 启动，通过 `--add-dir=<path>` 接收其余 Task Worktrees 和 Task Workspace。
- Codex 通过 `-C` 接收第一个 Task Worktree，通过重复的 `--add-dir` 接收其余 Task Worktrees 和 Task Workspace。
- 对于空 Task，三者都从 Task Workspace 启动。

第一个 Task Worktree 由 Repository Attachment 创建顺序决定。Launcher 参数必须放在 `--` 之后。Claude 的 worktree 选项和 Codex 的 working-root 选项会被拒绝，因为该布局由 devtask 管理；允许额外的 `--add-dir`。Agent Launcher 成功启动后会返回子进程的精确退出码，并在子进程退出前持有 Task 和 Registered Repository shared locks。

## 状态与自动化

```sh
devtask repo list --json
devtask list --json
devtask status billing-rollout --json
```

人类可读输出保持简洁，并可能使用 `~` 缩写 home 目录下的路径。JSON 使用稳定的 snake_case 字段、绝对路径、UTC RFC 3339 时间戳和相互独立的显式状态 flags。`status` 可以同时报告 `modified`、`staged`、`untracked` 和 `conflicted`；`missing`、`unknown` 与 `incomplete` 表示结构或检查问题。

数据写入 stdout，诊断信息写入 stderr。devtask 自身的退出码如下：

| 退出码 | 含义 |
| ---: | --- |
| `0` | 成功 |
| `1` | Git、文件系统、lock 或 metadata 操作失败 |
| `2` | CLI 用法、无效配置或不安全/无效请求 |

Agent Launcher 启动后，程序会原样返回子进程退出码。启动诊断会指出可执行文件，但不会重复打印传入 Agent 的参数。

## 安全删除

删除一个 Repository Attachment 或整个 Task：

```sh
devtask remove-repo billing-rollout web
devtask remove billing-rollout
```

删除前，devtask 会检查精确的 Main Checkout、Task Worktree 路径、Git worktree record、Task Branch Name、Task Workspace link、Git status、ignored content 和 Task Context Files。存在 modified、staged、untracked、conflicted 或未知 ignored content 时，普通删除会被阻止。编辑或新增的 Task Context Files 也会阻止整个 Task 的普通删除。

危险能力必须显式请求，程序不会现场询问：

- `--force` 允许删除受保护的 Task Worktree 内容；删除整个 Task 时，也允许删除受保护的 Task Context Files。它永远不会绕过路径 containment、Main Checkout 保护、对象 identity 或 locks。
- `--delete-branch` 请求删除 Task Branch Name。该分支必须已合并进当前 Base Ref；如果要删除未合并分支，还必须同时使用 `--force`。
- `--forget` 仅在 Task Worktree 路径和 Git worktree record 都已经不存在时，从 metadata 中移除一个 Repository Attachment。它不能与 `--delete-branch` 同时使用。

示例：

```sh
# 保留 Task Branch Names（默认行为）。
devtask remove billing-rollout

# 仅在验证已合并后删除 Task Branch Names。
devtask remove billing-rollout --delete-branch

# 显式允许丢失本地数据并删除未合并分支。
devtask remove billing-rollout --delete-branch --force
```

## Incomplete 状态与恢复

跨仓库操作使用 preflight 和 best-effort compensation；彼此独立的 Git 仓库无法组成一个原子事务。如果 compensation 或不可逆删除步骤失败，devtask 会把 Task 或 Repository Attachment 记录为 `incomplete`，并保存最后一次错误、residual objects 和 recovery instructions。

首先运行：

```sh
devtask status <task>
devtask status <task> --json
```

修改任何内容前，先检查输出中的每个路径和 Git worktree record。Task 仍处于 incomplete 时，不要重新运行 `add`、启动 Agent Launcher 或强制执行普通删除。按照输出中的 recovery instructions 操作：

- 如果预期对象仍包含需要保留的工作，恢复该对象。
- 只有在验证 residual object 的 identity 和内容后才能删除它。
- 外部清理同时移除了 Task Worktree 路径和 Git record 后，运行 `devtask remove-repo <task> <alias> --forget`，使 metadata 与 Task Workspace projection 恢复一致。
- 如果中断的 grouped creation 只留下 Task Workspace 或 metadata residuals，且没有可恢复的 Repository Attachment，请先备份有用的 Task Context Files，核对 `status` 输出的精确 residual paths，再手动清理，之后重新创建 Task。

v1 没有通用自动修复命令。这是有意的：当 Git ownership 无法被证明时，devtask 只报告事实，不会接管或删除未知状态。

## 命令参考

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

Task names、Repository Aliases 和 Repository Group names 使用 `[A-Za-z0-9][A-Za-z0-9._-]*`；`.` 和 `..` 无效，冲突按大小写不敏感方式检查。Repository Alias 也不能与 `TASK.md`、`AGENTS.md` 等 Task Context File 名称冲突。
