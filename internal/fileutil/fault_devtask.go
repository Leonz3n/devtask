//go:build devtask_test

package fileutil

import (
	"errors"
	"os"
)

func afterPublishForTest(path string) error {
	if os.Getenv("DEVTASK_TEST_FAIL_SYNC_AFTER_PUBLISH") == path {
		return errors.New("injected directory sync failure after publish")
	}
	return nil
}
