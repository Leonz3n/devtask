# Snapshot repository details in task attachments

A Task records each Repository Attachment's alias, repository root, worktree path, Task Branch Name, Base Ref, and creation order when it is attached. Later registry or Repository Group changes do not mutate existing Tasks, because silently redirecting established work to another repository or changing a Task's repository membership would make local state unpredictable and unsafe.
