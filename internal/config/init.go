package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

var ErrInvalid = errors.New("invalid configuration")

const seedConfiguration = `schema_version: 1

defaults:
  base_branch: main
  branch_pattern: "feat/{{.Task}}"
  remote: origin
  fetch: true

agents:
  pi:
    command: pi
  claude:
    command: claude
  codex:
    command: codex

repositories: {}
groups: {}
`

func Initialize(paths Paths) error {
	if err := os.MkdirAll(paths.ConfigDir, 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	lock, err := acquireLock(paths.LockFile)
	if err != nil {
		return err
	}
	defer lock.close()

	if _, err := os.Stat(paths.ConfigFile); errors.Is(err, os.ErrNotExist) {
		if err := writeAtomic(paths.ConfigFile, []byte(seedConfiguration), 0o600); err != nil {
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

type fileLock struct {
	file *os.File
}

func acquireLock(path string) (*fileLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open config lock: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("config is busy: another devtask process holds %s", path)
		}
		return nil, fmt.Errorf("lock configuration: %w", err)
	}
	return &fileLock{file: file}, nil
}

func (lock *fileLock) close() {
	_ = unix.Flock(int(lock.file.Fd()), unix.LOCK_UN)
	_ = lock.file.Close()
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
