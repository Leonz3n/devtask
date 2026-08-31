//go:build darwin

package config

import "golang.org/x/sys/unix"

func exchangeFiles(first, second string) error {
	return unix.RenamexNp(first, second, unix.RENAME_SWAP)
}
