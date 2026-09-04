package config

import (
	"errors"
	"fmt"
	"os"

	"github.com/Leonz3n/devtask/internal/fileutil"
	"github.com/Leonz3n/devtask/internal/lock"
)

var ErrInvalid = errors.New("invalid configuration")

func Initialize(paths Paths) error {
	if err := os.MkdirAll(paths.ConfigDir, 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	configLock, err := lock.Acquire(paths.LockFile)
	if err != nil {
		if errors.Is(err, lock.ErrBusy) {
			return fmt.Errorf("config is busy: another devtask process holds %s", paths.LockFile)
		}
		return err
	}
	defer configLock.Close()

	configuration := Default()
	if _, err := os.Stat(paths.ConfigFile); errors.Is(err, os.ErrNotExist) {
		contents, err := marshal(Default())
		if err != nil {
			return err
		}
		if err := writeAtomic(paths.ConfigFile, contents, 0o600); err != nil {
			return fmt.Errorf("create configuration: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("inspect configuration: %w", err)
	} else {
		configuration, err = Load(paths.ConfigFile)
		if err != nil {
			return err
		}
	}

	paths = paths.WithTaskWorkspaceRoot(configuration)
	for _, directory := range []string{paths.DataDir, paths.TasksDir, paths.Workspaces} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return fmt.Errorf("create state directory %q: %w", directory, err)
		}
	}
	return nil
}

func writeAtomic(path string, contents []byte, mode os.FileMode) error {
	_, err := fileutil.WriteAtomic(path, contents, mode)
	return err
}

func writeAtomicIfUnchanged(path string, original, contents []byte, mode os.FileMode) error {
	_, err := fileutil.WriteAtomicIfUnchanged(path, original, contents, mode)
	if errors.Is(err, fileutil.ErrConcurrentChange) {
		return ErrConcurrentEdit
	}
	return err
}
