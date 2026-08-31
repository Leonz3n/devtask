package lock

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

var ErrBusy = errors.New("lock is busy")

type File struct {
	file *os.File
}

func Acquire(path string) (*File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock %q: %w", path, err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: %s", ErrBusy, path)
		}
		return nil, fmt.Errorf("acquire lock %q: %w", path, err)
	}
	return &File{file: file}, nil
}

func (lock *File) Close() error {
	unlockError := unix.Flock(int(lock.file.Fd()), unix.LOCK_UN)
	closeError := lock.file.Close()
	if unlockError != nil {
		return fmt.Errorf("release lock: %w", unlockError)
	}
	if closeError != nil {
		return fmt.Errorf("close lock: %w", closeError)
	}
	return nil
}
