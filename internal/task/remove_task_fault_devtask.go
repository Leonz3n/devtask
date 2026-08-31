//go:build devtask_test

package task

import (
	"fmt"
	"os"
)

func beforeTaskAttachmentRemovalForTest(alias string) error {
	if os.Getenv("DEVTASK_TEST_FAIL_TASK_REMOVE_ALIAS") == alias {
		return fmt.Errorf("injected Task removal failure for %s", alias)
	}
	return nil
}

func afterTaskAttachmentWorktreeRemovalForTest(alias string) error {
	if os.Getenv("DEVTASK_TEST_FAIL_TASK_REMOVE_AFTER_WORKTREE_ALIAS") == alias {
		return fmt.Errorf("injected failure after %s Task Worktree removal", alias)
	}
	return nil
}
