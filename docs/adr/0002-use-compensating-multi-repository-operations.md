# Use compensating multi-repository operations

Operations spanning repositories use a complete preflight followed by ordered changes, with best-effort rollback limited to objects created by the current command. Git repositories cannot participate in one atomic transaction, so devtask records every completed or failed step and persists the observed state if rollback is incomplete instead of claiming an all-or-nothing result.

A Task and each affected Repository Attachment can be marked `incomplete` with the last error and observed residual state. Normal commands do not treat incomplete state as success; status reports it and provides manual recovery instructions.

## Consequences

Removal cannot restore a worktree after a later repository fails. It therefore preflights every attachment, records each successful removal immediately, and reports any remaining state precisely.
