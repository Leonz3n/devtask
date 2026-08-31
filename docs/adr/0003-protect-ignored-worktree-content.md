# Protect ignored worktree content during removal

devtask treats unknown ignored content as local data that blocks Task Worktree removal unless force is explicit. Git reports a worktree containing only ignored files as clean and can delete those files during an ordinary worktree removal, so relying on the usual dirty status would violate devtask's conservative deletion contract; devtask-managed Shared Local Path links are the only default exception.
