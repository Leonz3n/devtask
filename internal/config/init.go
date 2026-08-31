package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

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
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	keep = true
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
