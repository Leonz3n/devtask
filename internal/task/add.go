package task

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Leonz3n/devtask/internal/config"
	"github.com/Leonz3n/devtask/internal/fileutil"
	gitcmd "github.com/Leonz3n/devtask/internal/git"
	"github.com/Leonz3n/devtask/internal/lock"
	"github.com/Leonz3n/devtask/internal/repo"
	"github.com/Leonz3n/devtask/internal/workspace"
	"gopkg.in/yaml.v3"
)

type AddResult struct {
	TaskName        string
	Attachment      RepositoryAttachment
	AlreadyAttached bool
}

func AddRepository(paths config.Paths, configuration config.Config, taskName, repositoryAlias string, baseOverride *string) (AddResult, error) {
	if err := ValidateName(taskName); err != nil {
		return AddResult{}, err
	}
	taskLock, err := lock.Acquire(lockPath(paths, taskName))
	if err != nil {
		if errors.Is(err, lock.ErrBusy) {
			return AddResult{}, fmt.Errorf("Task %q is busy: another devtask process holds its lock", taskName)
		}
		return AddResult{}, err
	}
	defer taskLock.Close()

	metadataPath, originalMetadata, metadata, err := loadForUpdate(paths, taskName)
	if err != nil {
		return AddResult{}, err
	}
	alias, repositoryConfiguration, err := findRegisteredRepository(configuration, repositoryAlias)
	if err != nil {
		return AddResult{}, err
	}
	mainCheckout, err := repo.ResolveMainCheckout(repositoryConfiguration.Path)
	if err != nil {
		return AddResult{}, fmt.Errorf("inspect Registered Repository %q: %w", alias, err)
	}
	expectedWorktree, err := containedWorktreePath(mainCheckout, metadata.Name)
	if err != nil {
		return AddResult{}, err
	}
	for _, attachment := range metadata.Attachments {
		if !strings.EqualFold(attachment.Alias, alias) {
			continue
		}
		if err := verifyExistingAttachment(metadata, attachment, mainCheckout, expectedWorktree, paths); err != nil {
			return AddResult{}, err
		}
		return AddResult{TaskName: metadata.Name, Attachment: attachment, AlreadyAttached: true}, nil
	}

	repositoryLockPath, err := gitcmd.RepositoryLockPath(mainCheckout)
	if err != nil {
		return AddResult{}, err
	}
	repositoryLock, err := lock.Acquire(repositoryLockPath)
	if err != nil {
		if errors.Is(err, lock.ErrBusy) {
			return AddResult{}, fmt.Errorf("Registered Repository %q is busy: another devtask process holds its lock", alias)
		}
		return AddResult{}, err
	}
	defer repositoryLock.Close()

	baseBranch := configuration.Defaults.BaseBranch
	if repositoryConfiguration.BaseBranch != "" {
		baseBranch = repositoryConfiguration.BaseBranch
	}
	if baseOverride != nil {
		baseBranch = *baseOverride
	}
	if strings.TrimSpace(baseBranch) == "" {
		return AddResult{}, invalid("Base Ref for repository %q must be a non-empty branch name", alias)
	}
	if err := gitcmd.ValidateBranchName(baseBranch); err != nil {
		return AddResult{}, invalid("invalid Base Ref for repository %q: %v", alias, err)
	}
	baseRef := "refs/heads/" + baseBranch
	baseCommitOutput, err := gitcmd.Run(mainCheckout, "rev-parse", "--verify", baseRef+"^{commit}")
	if err != nil {
		return AddResult{}, invalid("local Base Ref %q for repository %q does not exist: %v", baseBranch, alias, err)
	}
	baseCommit := strings.TrimSpace(string(baseCommitOutput))
	branchRef := "refs/heads/" + metadata.TaskBranchName
	if exists, err := gitcmd.RefExists(mainCheckout, branchRef); err != nil {
		return AddResult{}, fmt.Errorf("inspect Task Branch Name %q in repository %q: %w", metadata.TaskBranchName, alias, err)
	} else if exists {
		return AddResult{}, invalid("Task Branch Name %q already exists in repository %q; existing branch attachment is not supported yet", metadata.TaskBranchName, alias)
	}
	worktreesRoot := filepath.Dir(expectedWorktree)
	rootCreated, err := preflightWorktreeRoot(worktreesRoot)
	if err != nil {
		return AddResult{}, err
	}
	if _, err := os.Lstat(expectedWorktree); err == nil {
		return AddResult{}, invalid("Task Worktree collision at %q", expectedWorktree)
	} else if !errors.Is(err, os.ErrNotExist) {
		return AddResult{}, fmt.Errorf("inspect Task Worktree path %q: %w", expectedWorktree, err)
	}
	workspacePath := filepath.Join(paths.Workspaces, metadata.Name)
	projectionAttachments := make([]workspace.Attachment, 0, len(metadata.Attachments)+1)
	for _, attachment := range metadata.Attachments {
		projectionAttachments = append(projectionAttachments, workspace.Attachment{Alias: attachment.Alias, WorktreePath: attachment.WorktreePath})
	}
	projectionAttachments = append(projectionAttachments, workspace.Attachment{Alias: alias, WorktreePath: expectedWorktree})
	projection, err := workspace.PrepareProjection(workspacePath, metadata.Name, metadata.TaskBranchName, alias, expectedWorktree, projectionAttachments)
	if err != nil {
		if errors.Is(err, workspace.ErrCollision) {
			return AddResult{}, invalid("%v", err)
		}
		return AddResult{}, err
	}

	excludeChange, err := gitcmd.EnsureWorktreesIgnored(mainCheckout)
	if err != nil {
		return AddResult{}, err
	}
	rollback := func(cause error, worktreeCreated bool) error {
		var rollbackErrors []error
		rollbackErrors = append(rollbackErrors, projection.Abort())
		if worktreeCreated {
			if removeError := gitcmd.RemoveWorktree(mainCheckout, expectedWorktree); removeError != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("remove Task Worktree: %w", removeError))
			}
			if deleteError := gitcmd.DeleteBranch(mainCheckout, metadata.TaskBranchName); deleteError != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("delete Task Branch Name: %w", deleteError))
			}
		}
		if rootCreated {
			if removeError := os.Remove(worktreesRoot); removeError != nil && !errors.Is(removeError, os.ErrNotExist) {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("remove empty worktrees directory: %w", removeError))
			}
		}
		rollbackErrors = append(rollbackErrors, excludeChange.Abort())
		if joined := errors.Join(rollbackErrors...); joined != nil {
			return fmt.Errorf("%v; roll back Repository Attachment: %w", cause, joined)
		}
		return cause
	}

	if err := gitcmd.CreateWorktree(mainCheckout, expectedWorktree, metadata.TaskBranchName, baseRef); err != nil {
		return AddResult{}, rollback(fmt.Errorf("create Task Worktree for repository %q: %w", alias, err), false)
	}
	canonicalWorktree, err := filepath.EvalSymlinks(expectedWorktree)
	if err != nil {
		return AddResult{}, rollback(fmt.Errorf("resolve created Task Worktree: %w", err), true)
	}
	if canonicalWorktree != expectedWorktree {
		return AddResult{}, rollback(fmt.Errorf("created Task Worktree resolved outside its expected path: %q", canonicalWorktree), true)
	}
	if err := projection.Commit(); err != nil {
		return AddResult{}, rollback(err, true)
	}
	attachment := RepositoryAttachment{
		Alias:          alias,
		MainCheckout:   mainCheckout,
		WorktreePath:   canonicalWorktree,
		TaskBranchName: metadata.TaskBranchName,
		BaseBranch:     baseBranch,
		BaseRef:        baseRef,
		BaseCommit:     baseCommit,
		Order:          len(metadata.Attachments),
		BranchExisted:  false,
		ManagedLinks:   make([]workspace.ManagedLink, 0),
	}
	metadata.Attachments = append(metadata.Attachments, attachment)
	metadata.ContextFiles = projection.RefreshOwnedContextFiles(metadata.ContextFiles)
	updatedMetadata, err := yaml.Marshal(metadata)
	if err != nil {
		return AddResult{}, rollback(fmt.Errorf("encode Task metadata: %w", err), true)
	}
	if err := writeAtomicReplaceIfUnchanged(metadataPath, originalMetadata, updatedMetadata); err != nil {
		return AddResult{}, rollback(fmt.Errorf("update Task metadata: %w", err), true)
	}
	return AddResult{TaskName: metadata.Name, Attachment: attachment}, nil
}

func findRegisteredRepository(configuration config.Config, requested string) (string, config.RepositoryConfig, error) {
	for alias, repository := range configuration.Repositories {
		if strings.EqualFold(alias, requested) {
			return alias, repository, nil
		}
	}
	return "", config.RepositoryConfig{}, invalid("unknown Repository Alias %q", requested)
}

func loadForUpdate(paths config.Paths, requested string) (string, []byte, Metadata, error) {
	entries, err := os.ReadDir(paths.TasksDir)
	if err != nil {
		return "", nil, Metadata{}, fmt.Errorf("inspect Tasks: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".yaml")
		if !strings.EqualFold(name, requested) {
			continue
		}
		path := filepath.Join(paths.TasksDir, entry.Name())
		contents, err := os.ReadFile(path)
		if err != nil {
			return "", nil, Metadata{}, fmt.Errorf("read Task metadata %q: %w", path, err)
		}
		metadata, err := load(path)
		return path, contents, metadata, err
	}
	return "", nil, Metadata{}, invalid("Task %q does not exist", requested)
}

func containedWorktreePath(mainCheckout, taskName string) (string, error) {
	root := filepath.Join(mainCheckout, ".worktrees")
	target := filepath.Join(root, taskName)
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", invalid("Task Worktree path %q is outside %q", target, root)
	}
	return target, nil
}

func preflightWorktreeRoot(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect worktrees directory %q: %w", path, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, invalid("worktrees path %q must be a real directory", path)
	}
	return false, nil
}

func writeAtomicReplaceIfUnchanged(path string, original, updated []byte) error {
	current, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, original) {
		return errors.New("Task metadata changed while it was being updated; retry the command")
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".task-update-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(updated); err != nil {
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
	return fileutil.SyncDirectory(directory)
}

func verifyExistingAttachment(metadata Metadata, attachment RepositoryAttachment, mainCheckout, expectedWorktree string, paths config.Paths) error {
	if attachment.MainCheckout != mainCheckout || attachment.WorktreePath != expectedWorktree || attachment.TaskBranchName != metadata.TaskBranchName {
		return invalid("Repository Attachment %q does not match its recorded ownership; run status for recovery guidance", attachment.Alias)
	}
	canonical, err := filepath.EvalSymlinks(expectedWorktree)
	if err != nil || canonical != expectedWorktree {
		return invalid("Repository Attachment %q Task Worktree is missing or changed", attachment.Alias)
	}
	branch, err := gitcmd.Run(expectedWorktree, "branch", "--show-current")
	if err != nil || strings.TrimSpace(string(branch)) != metadata.TaskBranchName {
		return invalid("Repository Attachment %q is not on Task Branch Name %q", attachment.Alias, metadata.TaskBranchName)
	}
	linkPath := filepath.Join(paths.Workspaces, metadata.Name, attachment.Alias)
	resolved, err := filepath.EvalSymlinks(linkPath)
	if err != nil || resolved != expectedWorktree {
		return invalid("Repository Attachment %q Task Workspace link is missing or changed", attachment.Alias)
	}
	return nil
}
