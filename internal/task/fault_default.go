//go:build !devtask_test

package task

func interruptAfterWorkspaceForTest() {}

func afterProjectionForTest() error { return nil }

func afterRepositoryLocksForTest([]string) error { return nil }

func afterExcludeForTest(string) error { return nil }

func afterWorktreeForTest(string) error { return nil }

func beforeWorktreeMoveForTest(string) {}

func beforeCompensationForTest(string) {}

func recordCompensationForTest(string) error { return nil }
