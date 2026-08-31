//go:build linux

package config

import "golang.org/x/sys/unix"

func exchangeFiles(first, second string) error {
	return unix.Renameat2(unix.AT_FDCWD, first, unix.AT_FDCWD, second, unix.RENAME_EXCHANGE)
}
