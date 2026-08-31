package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

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
	} else if _, err := Load(paths.ConfigFile); err != nil {
		return err
	}

	for _, directory := range []string{paths.DataDir, paths.TasksDir, paths.Workspaces} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return fmt.Errorf("create state directory %q: %w", directory, err)
		}
	}
	return nil
}

func writeAtomic(path string, contents []byte, mode os.FileMode) error {
	return writeAtomicChecked(path, contents, mode, nil)
}

func writeAtomicIfUnchanged(path string, original, contents []byte, mode os.FileMode) error {
	return writeAtomicChecked(path, contents, mode, original)
}

func writeAtomicChecked(path string, contents []byte, mode os.FileMode, original []byte) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".config.yaml-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(mode); err != nil {
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if original == nil {
		if err := os.Rename(temporaryPath, path); err != nil {
			return err
		}
		keep = true
	} else {
		if err := exchangeFiles(temporaryPath, path); err != nil {
			return fmt.Errorf("atomically exchange configuration: %w", err)
		}
		// After the exchange, the temporary path owns user data. It must only be
		// removed explicitly after validation or after a successful rollback.
		keep = true
		displaced, err := os.ReadFile(temporaryPath)
		if err != nil {
			if rollbackError := restoreDisplacedConfiguration(temporaryPath, path, directory); rollbackError != nil {
				return fmt.Errorf("inspect displaced configuration: %v; restore it: %w", err, rollbackError)
			}
			return fmt.Errorf("inspect displaced configuration: %w", err)
		}
		if !bytes.Equal(displaced, original) {
			if err := restoreDisplacedConfiguration(temporaryPath, path, directory); err != nil {
				return fmt.Errorf("%w; restore external edit: %v", ErrConcurrentEdit, err)
			}
			return ErrConcurrentEdit
		}
		if err := removeAndSync(temporaryPath, directory); err != nil {
			return fmt.Errorf("remove displaced configuration: %w", err)
		}
	}
	return fileutil.SyncDirectory(directory)
}

func restoreDisplacedConfiguration(temporaryPath, path, directory string) error {
	if err := exchangeFiles(temporaryPath, path); err != nil {
		return err
	}
	return removeAndSync(temporaryPath, directory)
}

func removeAndSync(path, directory string) error {
	removeError := os.Remove(path)
	syncError := fileutil.SyncDirectory(directory)
	return errors.Join(removeError, syncError)
}
