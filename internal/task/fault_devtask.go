//go:build devtask_test

package task

import (
	"errors"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func interruptAfterWorkspaceForTest() {
	if os.Getenv("DEVTASK_TEST_INTERRUPT_AFTER_WORKSPACE") == "1" {
		_ = unix.Kill(os.Getpid(), unix.SIGKILL)
	}
	if signalPath := os.Getenv("DEVTASK_TEST_PAUSE_AFTER_WORKSPACE"); signalPath != "" {
		_ = os.WriteFile(signalPath+".ready", nil, 0o600)
		for {
			if _, err := os.Stat(signalPath + ".continue"); err == nil {
				return
			}
			time.Sleep(time.Millisecond)
		}
	}
}

func afterProjectionForTest() error {
	if signalPath := os.Getenv("DEVTASK_TEST_PAUSE_AFTER_PROJECTION"); signalPath != "" {
		_ = os.WriteFile(signalPath+".ready", nil, 0o600)
		for {
			if _, err := os.Stat(signalPath + ".continue"); err == nil {
				break
			}
			time.Sleep(time.Millisecond)
		}
	}
	if os.Getenv("DEVTASK_TEST_FAIL_AFTER_PROJECTION") == "1" {
		return errors.New("injected metadata update failure")
	}
	return nil
}

func afterRepositoryLocksForTest(paths []string) error {
	if outputPath := os.Getenv("DEVTASK_TEST_RECORD_REPOSITORY_LOCKS"); outputPath != "" {
		if err := os.WriteFile(outputPath, []byte(strings.Join(paths, "\n")+"\n"), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func afterExcludeForTest(alias string) error {
	if os.Getenv("DEVTASK_TEST_FAIL_AFTER_EXCLUDE_ALIAS") == alias {
		return errors.New("injected failure after local exclude update for " + alias)
	}
	return nil
}

func afterWorktreeForTest(alias string) error {
	if os.Getenv("DEVTASK_TEST_FAIL_AFTER_WORKTREE_ALIAS") == alias {
		return errors.New("injected failure after Task Worktree creation for " + alias)
	}
	return nil
}

func beforeWorktreeMoveForTest(alias string) {
	if os.Getenv("DEVTASK_TEST_PAUSE_BEFORE_WORKTREE_MOVE_ALIAS") != alias {
		return
	}
	signalPath := os.Getenv("DEVTASK_TEST_PAUSE_BEFORE_WORKTREE_MOVE")
	if signalPath == "" {
		return
	}
	_ = os.WriteFile(signalPath+".ready", nil, 0o600)
	for {
		if _, err := os.Stat(signalPath + ".continue"); err == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

func beforeCompensationForTest(alias string) {
	if os.Getenv("DEVTASK_TEST_PAUSE_BEFORE_COMPENSATION_ALIAS") != alias {
		return
	}
	signalPath := os.Getenv("DEVTASK_TEST_PAUSE_BEFORE_COMPENSATION")
	if signalPath == "" {
		return
	}
	_ = os.WriteFile(signalPath+".ready", nil, 0o600)
	for {
		if _, err := os.Stat(signalPath + ".continue"); err == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

func recordCompensationForTest(alias string) error {
	outputPath := os.Getenv("DEVTASK_TEST_RECORD_COMPENSATION")
	if outputPath == "" {
		return nil
	}
	file, err := os.OpenFile(outputPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.WriteString(alias + "\n"); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
