//go:build devtask_test

package task

import (
	"errors"
	"os"
)

func afterWorktreeRemovalForTest() error {
	if os.Getenv("DEVTASK_TEST_FAIL_AFTER_WORKTREE_REMOVAL") == "1" {
		return errors.New("injected failure after Task Worktree removal")
	}
	return nil
}
