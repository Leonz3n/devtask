package workspace

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Leonz3n/devtask/internal/fileutil"
)

const (
	GeneratedStart = "<!-- devtask:generated:start -->"
	GeneratedEnd   = "<!-- devtask:generated:end -->"
)

var ErrCollision = errors.New("Task Workspace collision")

type ContextFile struct {
	Path   string `yaml:"path"`
	SHA256 string `yaml:"sha256"`
}

type Prepared struct {
	Path         string
	stagingPath  string
	ContextFiles []ContextFile
	created      bool
	owned        os.FileInfo
	published    os.FileInfo
	expected     []generatedFile
}

type generatedFile struct {
	name     string
	contents []byte
}

func Prepare(root, taskName, taskBranchName string) (*Prepared, error) {
	path := filepath.Join(root, taskName)
	expected := generatedFiles(taskName, taskBranchName)
	if info, err := os.Lstat(path); err == nil {
		if err := verifyGeneratedWorkspace(path, info, expected); err != nil {
			return nil, fmt.Errorf("%w at %q: %v", ErrCollision, path, err)
		}
		return &Prepared{Path: path, ContextFiles: contextFiles(expected), published: info, expected: expected}, nil
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect Task Workspace %q: %w", path, err)
	}

	stagingPath, err := os.MkdirTemp(root, ".devtask-new-*")
	if err != nil {
		return nil, fmt.Errorf("prepare Task Workspace: %w", err)
	}
	prepared := &Prepared{Path: path, stagingPath: stagingPath, created: true, expected: expected}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(stagingPath)
		}
	}()
	if err := os.Chmod(stagingPath, 0o700); err != nil {
		return nil, fmt.Errorf("set Task Workspace permissions: %w", err)
	}

	for _, file := range expected {
		if err := os.WriteFile(filepath.Join(stagingPath, file.name), file.contents, 0o600); err != nil {
			return nil, fmt.Errorf("create Task Context File %q: %w", file.name, err)
		}
	}
	owned, err := os.Lstat(stagingPath)
	if err != nil {
		return nil, fmt.Errorf("inspect staged Task Workspace: %w", err)
	}
	prepared.owned = owned
	prepared.ContextFiles = contextFiles(expected)
	cleanup = false
	return prepared, nil
}

func (prepared *Prepared) Commit() error {
	if !prepared.created {
		current, err := os.Lstat(prepared.Path)
		if err != nil {
			return fmt.Errorf("inspect recoverable Task Workspace: %w", err)
		}
		if prepared.published == nil || !os.SameFile(prepared.published, current) {
			return fmt.Errorf("%w at %q: ownership changed", ErrCollision, prepared.Path)
		}
		if err := verifyGeneratedWorkspace(prepared.Path, current, prepared.expected); err != nil {
			return fmt.Errorf("%w at %q: %v", ErrCollision, prepared.Path, err)
		}
		return nil
	}
	if err := fileutil.RenameExclusive(prepared.stagingPath, prepared.Path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w at %q", ErrCollision, prepared.Path)
		}
		return fmt.Errorf("publish Task Workspace: %w", err)
	}
	prepared.stagingPath = ""
	info, err := os.Lstat(prepared.Path)
	if err != nil {
		return fmt.Errorf("inspect published Task Workspace: %w", err)
	}
	if prepared.owned == nil || !os.SameFile(prepared.owned, info) {
		return fmt.Errorf("published Task Workspace %q changed identity", prepared.Path)
	}
	prepared.published = prepared.owned
	return fileutil.SyncDirectory(filepath.Dir(prepared.Path))
}

func (prepared *Prepared) Abort() error {
	if !prepared.created {
		return nil
	}
	path := prepared.stagingPath
	if path != "" {
		current, err := os.Lstat(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("inspect staged Task Workspace %q: %w", path, err)
		}
		if prepared.owned == nil || !os.SameFile(prepared.owned, current) {
			return fmt.Errorf("refuse to remove changed staged Task Workspace %q", path)
		}
		if err := verifyGeneratedWorkspace(path, current, prepared.expected); err != nil {
			return fmt.Errorf("refuse to remove changed staged Task Workspace %q: %w", path, err)
		}
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("remove staged Task Workspace %q: %w", path, err)
		}
		return fileutil.SyncDirectory(filepath.Dir(prepared.Path))
	}
	current, err := os.Lstat(prepared.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("inspect incomplete Task Workspace %q: %w", prepared.Path, err)
	}
	if prepared.published == nil || !os.SameFile(prepared.published, current) {
		return fmt.Errorf("refuse to remove changed Task Workspace %q", prepared.Path)
	}
	if err := verifyGeneratedWorkspace(prepared.Path, current, prepared.expected); err != nil {
		return fmt.Errorf("refuse to remove changed Task Workspace %q: %w", prepared.Path, err)
	}
	if err := os.RemoveAll(prepared.Path); err != nil {
		return fmt.Errorf("remove incomplete Task Workspace %q: %w", prepared.Path, err)
	}
	return fileutil.SyncDirectory(filepath.Dir(prepared.Path))
}

func generatedFiles(taskName, taskBranchName string) []generatedFile {
	return []generatedFile{
		{name: "TASK.md", contents: []byte(fmt.Sprintf("# %s\n", taskName))},
		{name: "AGENTS.md", contents: []byte(fmt.Sprintf("# Task Context\n\n%s\n## Task\n\n- Name: `%s`\n- Task Branch Name: `%s`\n\n## Repository Attachments\n\nNone.\n%s\n", GeneratedStart, taskName, taskBranchName, GeneratedEnd))},
	}
}

func contextFiles(files []generatedFile) []ContextFile {
	result := make([]ContextFile, 0, len(files))
	for _, file := range files {
		digest := sha256.Sum256(file.contents)
		result = append(result, ContextFile{Path: file.name, SHA256: hex.EncodeToString(digest[:])})
	}
	return result
}

func verifyGeneratedWorkspace(path string, directory os.FileInfo, expected []generatedFile) error {
	if !directory.IsDir() || directory.Mode()&os.ModeSymlink != 0 {
		return errors.New("expected a generated directory")
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	if len(entries) != len(expected) {
		return fmt.Errorf("expected exactly %d generated files, found %d entries", len(expected), len(entries))
	}
	byName := make(map[string][]byte, len(expected))
	for _, file := range expected {
		byName[file.name] = file.contents
	}
	for _, entry := range entries {
		want, ok := byName[entry.Name()]
		if !ok {
			return fmt.Errorf("unexpected entry %q", entry.Name())
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("generated entry %q is not a regular file", entry.Name())
		}
		contents, err := os.ReadFile(filepath.Join(path, entry.Name()))
		if err != nil {
			return err
		}
		if !bytes.Equal(contents, want) {
			return fmt.Errorf("generated entry %q has changed", entry.Name())
		}
	}
	return nil
}
