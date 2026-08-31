//go:build !devtask_test

package task

func interruptAfterWorkspaceForTest() {}

func afterProjectionForTest() error { return nil }
