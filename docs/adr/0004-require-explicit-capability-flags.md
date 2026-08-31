# Require explicit capability flags instead of confirmation prompts

devtask does not ask interactive yes-or-no questions during core operations. A caller must explicitly request each dangerous capability with a narrow flag such as `--force` or `--delete-branch`; this keeps behavior identical for humans, scripts, and coding agents and prevents one broad confirmation from bypassing unrelated protections.

`--force` may authorize removal of dirty, untracked, or ignored Task Worktree content, but it never bypasses Main Checkout protection, path containment, object identity checks, metadata locking, or the separate requirement to request branch deletion.
