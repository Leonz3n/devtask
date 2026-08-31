package git

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var ErrWorktreeRecordNotFound = errors.New("Git worktree record not found")

type WorktreeRecord struct {
	Path      string
	BranchRef string
	Prunable  bool
}

type ExcludeUpdate struct {
	path     string
	original []byte
	updated  []byte
}

func RepositoryLockPath(mainCheckout string) (string, error) {
	output, err := Run(mainCheckout, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("locate Git common directory: %w", err)
	}
	gitDirectory := strings.TrimSpace(string(output))
	if !filepath.IsAbs(gitDirectory) {
		gitDirectory = filepath.Join(mainCheckout, gitDirectory)
	}
	canonical, err := filepath.EvalSymlinks(gitDirectory)
	if err != nil {
		return "", fmt.Errorf("resolve Git common directory %q: %w", gitDirectory, err)
	}
	return filepath.Join(canonical, "devtask.lock"), nil
}

func ListWorktrees(mainCheckout string) ([]WorktreeRecord, error) {
	output, err := Run(mainCheckout, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	blocks := bytes.Split(output, []byte{0, 0})
	records := make([]WorktreeRecord, 0, len(blocks))
	for _, block := range blocks {
		if len(block) == 0 {
			continue
		}
		var record WorktreeRecord
		for _, field := range bytes.Split(block, []byte{0}) {
			switch {
			case bytes.HasPrefix(field, []byte("worktree ")):
				record.Path = string(field[len("worktree "):])
			case bytes.HasPrefix(field, []byte("branch ")):
				record.BranchRef = string(field[len("branch "):])
			case bytes.HasPrefix(field, []byte("prunable ")):
				record.Prunable = true
			}
		}
		if record.Path == "" {
			return nil, errors.New("Git worktree record is missing its path")
		}
		records = append(records, record)
	}
	return records, nil
}

func WorktreeAt(mainCheckout, path string) (WorktreeRecord, error) {
	records, err := ListWorktrees(mainCheckout)
	if err != nil {
		return WorktreeRecord{}, err
	}
	cleanPath := filepath.Clean(path)
	for _, record := range records {
		if filepath.Clean(record.Path) == cleanPath {
			return record, nil
		}
	}
	return WorktreeRecord{}, ErrWorktreeRecordNotFound
}

func WorktreeForBranch(mainCheckout, branchRef string) (WorktreeRecord, error) {
	records, err := ListWorktrees(mainCheckout)
	if err != nil {
		return WorktreeRecord{}, err
	}
	for _, record := range records {
		if record.BranchRef == branchRef {
			return record, nil
		}
	}
	return WorktreeRecord{}, ErrWorktreeRecordNotFound
}

func EnsureWorktreesIgnored(mainCheckout string) (*ExcludeUpdate, error) {
	ignored, err := RunPredicate(mainCheckout, "check-ignore", "--no-index", "--quiet", ".worktrees/.devtask-probe")
	if err != nil {
		return nil, fmt.Errorf("check effective ignore for .worktrees: %w", err)
	}
	if ignored {
		return &ExcludeUpdate{}, nil
	}
	output, err := Run(mainCheckout, "rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return nil, fmt.Errorf("locate common local Git exclude: %w", err)
	}
	path := strings.TrimSpace(string(output))
	if !filepath.IsAbs(path) {
		path = filepath.Join(mainCheckout, path)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read common local Git exclude %q: %w", path, err)
	}
	addition := []byte("/.worktrees/\n")
	if len(original) > 0 && original[len(original)-1] != '\n' {
		addition = append([]byte{'\n'}, addition...)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return nil, fmt.Errorf("open common local Git exclude %q: %w", path, err)
	}
	if _, err := file.Write(addition); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("append common local Git exclude %q: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("sync common local Git exclude %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close common local Git exclude %q: %w", path, err)
	}
	updated := append(append([]byte(nil), original...), addition...)
	return &ExcludeUpdate{path: path, original: original, updated: updated}, nil
}

func (update *ExcludeUpdate) Abort() error {
	if update == nil || update.path == "" {
		return nil
	}
	current, err := os.ReadFile(update.path)
	if err != nil {
		return fmt.Errorf("read local exclude during rollback: %w", err)
	}
	if !bytes.Equal(current, update.updated) {
		return fmt.Errorf("refuse to restore changed local exclude %q", update.path)
	}
	file, err := os.OpenFile(update.path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return fmt.Errorf("restore local exclude: %w", err)
	}
	if _, err := file.Write(update.original); err != nil {
		_ = file.Close()
		return fmt.Errorf("restore local exclude: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync restored local exclude: %w", err)
	}
	return file.Close()
}

func CreateWorktree(mainCheckout, worktreePath, branch, baseRef string) error {
	_, err := Run(mainCheckout, "worktree", "add", "--no-track", "-b", branch, worktreePath, baseRef)
	return err
}

func AttachWorktree(mainCheckout, worktreePath, branch string) error {
	_, err := Run(mainCheckout, "worktree", "add", worktreePath, branch)
	return err
}

func RemoveWorktree(mainCheckout, worktreePath string) error {
	_, err := Run(mainCheckout, "worktree", "remove", "--force", worktreePath)
	return err
}

func DeleteBranch(mainCheckout, branch string) error {
	_, err := Run(mainCheckout, "branch", "-D", branch)
	return err
}
