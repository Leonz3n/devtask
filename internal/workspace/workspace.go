package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

const (
	GeneratedStart = "<!-- devtask:generated:start -->"
	GeneratedEnd   = "<!-- devtask:generated:end -->"
)

type ContextFile struct {
	Path   string `yaml:"path"`
	SHA256 string `yaml:"sha256"`
}

type Prepared struct {
	Path         string
	stagingPath  string
	ContextFiles []ContextFile
}

func Prepare(root, taskName, taskBranchName string) (*Prepared, error) {
	path := filepath.Join(root, taskName)
	if _, err := os.Lstat(path); err == nil {
		return nil, fmt.Errorf("Task Workspace collision at %q", path)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect Task Workspace %q: %w", path, err)
	}

	stagingPath, err := os.MkdirTemp(root, ".devtask-new-*")
	if err != nil {
		return nil, fmt.Errorf("prepare Task Workspace: %w", err)
	}
	prepared := &Prepared{Path: path, stagingPath: stagingPath}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(stagingPath)
		}
	}()
	if err := os.Chmod(stagingPath, 0o700); err != nil {
		return nil, fmt.Errorf("set Task Workspace permissions: %w", err)
	}

	files := []struct {
		name     string
		contents string
	}{
		{name: "TASK.md", contents: fmt.Sprintf("# %s\n", taskName)},
		{name: "AGENTS.md", contents: fmt.Sprintf("# Task Context\n\n%s\n## Task\n\n- Name: `%s`\n- Task Branch Name: `%s`\n\n## Repository Attachments\n\nNone.\n%s\n", GeneratedStart, taskName, taskBranchName, GeneratedEnd)},
	}
	for _, file := range files {
		if err := os.WriteFile(filepath.Join(stagingPath, file.name), []byte(file.contents), 0o600); err != nil {
			return nil, fmt.Errorf("create Task Context File %q: %w", file.name, err)
		}
		digest := sha256.Sum256([]byte(file.contents))
		prepared.ContextFiles = append(prepared.ContextFiles, ContextFile{Path: file.name, SHA256: hex.EncodeToString(digest[:])})
	}
	cleanup = false
	return prepared, nil
}

func (prepared *Prepared) Commit() error {
	if err := renameExclusive(prepared.stagingPath, prepared.Path); err != nil {
		return fmt.Errorf("publish Task Workspace: %w", err)
	}
	prepared.stagingPath = ""
	return syncDirectory(filepath.Dir(prepared.Path))
}

func (prepared *Prepared) Abort() error {
	path := prepared.stagingPath
	if path == "" {
		path = prepared.Path
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove incomplete Task Workspace %q: %w", path, err)
	}
	return syncDirectory(filepath.Dir(prepared.Path))
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
