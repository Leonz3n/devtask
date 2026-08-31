//go:build !devtask_test

package task

func beforeTaskAttachmentRemovalForTest(string) error        { return nil }
func afterTaskAttachmentWorktreeRemovalForTest(string) error { return nil }
