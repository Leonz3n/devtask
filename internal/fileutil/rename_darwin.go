package fileutil

import "golang.org/x/sys/unix"

func RenameExclusive(oldPath, newPath string) error {
	return unix.RenameatxNp(unix.AT_FDCWD, oldPath, unix.AT_FDCWD, newPath, unix.RENAME_EXCL)
}

func Exchange(first, second string) error {
	return unix.RenamexNp(first, second, unix.RENAME_SWAP)
}
