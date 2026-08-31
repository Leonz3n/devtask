# devtask

devtask coordinates local development work that spans multiple Git repositories while keeping each repository's history and working tree independent.

## Language

**Task**:
A named unit of local development that may span one or more Registered Repositories and share task-level context.
_Avoid_: Project, ticket, workspace

**Registered Repository**:
A local Git repository known to devtask by a unique Repository Alias and available to be attached to Tasks.
_Avoid_: Project, repo entry

**Repository Alias**:
The stable, human-facing name by which a Registered Repository is selected and displayed.
_Avoid_: Repository name, directory name

**Repository Attachment**:
The durable association between a Task and a Registered Repository, including the Task-specific Git work that belongs to that association.
_Avoid_: Task repository, linked repo

**Incomplete Attachment**:
A Repository Attachment whose intended state was not fully established or restored, and whose observed residual state requires explicit recovery.
_Avoid_: Failed repository, broken worktree

**Task Branch Name**:
The branch name shared by a Task's Repository Attachments, identifying independent branches with the same name in different Registered Repositories.
_Avoid_: Branch, feature branch

**Base Ref**:
The Git reference from which a new Task Branch is initially created in one Registered Repository.
_Avoid_: Main branch, upstream

**Main Checkout**:
The long-lived primary working tree of a Registered Repository, kept outside Task work and left on its normal base branch.
_Avoid_: Repository root, main worktree

**Task Worktree**:
The independent Git working tree belonging to one Repository Attachment. A Task has at most one Task Worktree for each Registered Repository.
_Avoid_: Checkout, workspace, repo copy

**Task Workspace**:
A Task-level view that brings its Task Worktrees and context files together without becoming a Git repository itself.
_Avoid_: Worktree, monorepo, checkout

**Task Context File**:
A file that describes or guides the Task as a whole rather than belonging to any one Registered Repository.
_Avoid_: Repository documentation

**Shared Local Path**:
A repository-specific, unversioned file or directory made available to Task Worktrees while remaining owned by the Main Checkout.
_Avoid_: Shared file, copied config

**Repository Group**:
A named, reusable selection of Registered Repositories that can be expanded when creating a Task.
_Avoid_: Task template, repository set

**Agent Launcher**:
An adapter that starts an external coding agent with access to a Task Workspace and its Task Worktrees.
_Avoid_: Agent, agent runtime
