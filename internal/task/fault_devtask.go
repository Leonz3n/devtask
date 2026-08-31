//go:build devtask_test

package task

import (
	"errors"
	"os"
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
