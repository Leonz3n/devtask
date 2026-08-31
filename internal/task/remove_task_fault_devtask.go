//go:build devtask_test

package task

import (
	"fmt"
	"os"
	"time"
)

func beforeTaskAttachmentRemovalForTest(alias string) error {
	if os.Getenv("DEVTASK_TEST_PAUSE_BEFORE_TASK_REMOVE_ALIAS") == alias {
		signal := os.Getenv("DEVTASK_TEST_TASK_REMOVE_SIGNAL")
		if signal == "" {
			return fmt.Errorf("missing Task removal test signal path")
		}
		if err := os.WriteFile(signal+".ready", nil, 0o600); err != nil {
			return fmt.Errorf("publish Task removal test boundary: %w", err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := os.Stat(signal + ".continue"); err == nil {
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("timed out at Task removal test boundary")
			}
			time.Sleep(time.Millisecond)
		}
	}
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
