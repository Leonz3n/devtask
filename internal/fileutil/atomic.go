package fileutil

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var ErrConcurrentChange = errors.New("file changed while it was being updated")

type WriteOutcome struct {
	Published bool
}

func WriteAtomic(path string, contents []byte, mode os.FileMode) (WriteOutcome, error) {
	return writeAtomic(path, nil, contents, mode)
}

func WriteAtomicIfUnchanged(path string, original, contents []byte, mode os.FileMode) (WriteOutcome, error) {
	return writeAtomic(path, original, contents, mode)
}

func writeAtomic(path string, original, contents []byte, mode os.FileMode) (WriteOutcome, error) {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".devtask-write-*")
	if err != nil {
		return WriteOutcome{}, err
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
		return WriteOutcome{}, err
	}
	if _, err := temporary.Write(contents); err != nil {
		return WriteOutcome{}, err
	}
	if err := temporary.Sync(); err != nil {
		return WriteOutcome{}, err
	}
	if err := temporary.Close(); err != nil {
		return WriteOutcome{}, err
	}
	if original == nil {
		if err := os.Rename(temporaryPath, path); err != nil {
			return WriteOutcome{}, err
		}
		keep = true
		if err := afterPublishForTest(path); err != nil {
			return WriteOutcome{Published: true}, err
		}
		return WriteOutcome{Published: true}, SyncDirectory(directory)
	}
	if err := Exchange(temporaryPath, path); err != nil {
		return WriteOutcome{}, fmt.Errorf("atomically exchange %q: %w", path, err)
	}
	keep = true
	displaced, err := os.ReadFile(temporaryPath)
	if err != nil {
		if rollbackError := restoreDisplaced(temporaryPath, path, directory); rollbackError != nil {
			return WriteOutcome{Published: true}, fmt.Errorf("inspect displaced file: %v; restore it: %w", err, rollbackError)
		}
		return WriteOutcome{}, fmt.Errorf("inspect displaced file: %w", err)
	}
	if !bytes.Equal(displaced, original) {
		if err := restoreDisplaced(temporaryPath, path, directory); err != nil {
			return WriteOutcome{Published: true}, fmt.Errorf("%w; restore external edit: %v", ErrConcurrentChange, err)
		}
		return WriteOutcome{}, ErrConcurrentChange
	}
	if err := os.Remove(temporaryPath); err != nil {
		return WriteOutcome{Published: true}, fmt.Errorf("remove displaced file: %w", err)
	}
	if err := afterPublishForTest(path); err != nil {
		return WriteOutcome{Published: true}, err
	}
	return WriteOutcome{Published: true}, SyncDirectory(directory)
}

func restoreDisplaced(temporaryPath, path, directory string) error {
	if err := Exchange(temporaryPath, path); err != nil {
		return err
	}
	return removeAndSync(temporaryPath, directory)
}

func removeAndSync(path, directory string) error {
	return errors.Join(os.Remove(path), SyncDirectory(directory))
}
