package task

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Leonz3n/devtask/internal/config"
	"github.com/Leonz3n/devtask/internal/fileutil"
	gitcmd "github.com/Leonz3n/devtask/internal/git"
	"github.com/Leonz3n/devtask/internal/lock"
)

type RemoveOptions struct {
	Force        bool
	DeleteBranch bool
	Fetch        *bool
}

type RemoveTaskRepositoryResult struct {
	RepositoryAlias string
	WorktreePath    string
	TaskBranchName  string
	WorktreeRemoved bool
	BranchDeleted   bool
	Completed       bool
}

type RemoveResult struct {
	TaskName     string
	Repositories []RemoveTaskRepositoryResult
}

type taskRemovalPlan struct {
	attachment RepositoryAttachment
	worktree   os.FileInfo
	baseRef    string
}

func Remove(paths config.Paths, configuration config.Config, taskName string, options RemoveOptions) (RemoveResult, error) {
	if err := ValidateName(taskName); err != nil {
		return RemoveResult{}, err
	}
	taskLock, err := lock.Acquire(lockPath(paths, taskName))
	if err != nil {
		if errors.Is(err, lock.ErrBusy) {
			return RemoveResult{}, fmt.Errorf("Task %q is busy: another devtask process holds its lock", taskName)
		}
		return RemoveResult{}, err
	}
	defer taskLock.Close()

	metadataPath, originalMetadata, metadata, err := loadForUpdate(paths, taskName)
	if err != nil {
		return RemoveResult{}, err
	}
	result := RemoveResult{TaskName: metadata.Name, Repositories: make([]RemoveTaskRepositoryResult, 0, len(metadata.Attachments))}

	lockTargets, err := taskRemovalLockTargets(metadata)
	if err != nil {
		return result, err
	}
	var lockFailures []error
	for _, target := range lockTargets {
		target.lock, err = lock.Acquire(target.path)
		if err != nil {
			if errors.Is(err, lock.ErrBusy) {
				lockFailures = append(lockFailures, fmt.Errorf("Registered Repository %q is busy: another devtask process holds its lock", strings.Join(target.aliases, ", ")))
			} else {
				lockFailures = append(lockFailures, err)
			}
		}
	}
	defer closeRepositoryLocks(lockTargets)
	if len(lockFailures) > 0 {
		return result, errors.Join(lockFailures...)
	}

	workspacePath := filepath.Join(paths.Workspaces, metadata.Name)
	var blockers []error
	workspaceInfo, protectedContext, err := preflightTaskWorkspace(paths, metadata)
	if err != nil {
		blockers = append(blockers, err)
	}
	if len(protectedContext) > 0 && !options.Force {
		blockers = append(blockers, invalid("Task %q contains protected Task Context Files (%s); inspect them or rerun with --force to authorize their removal", metadata.Name, strings.Join(protectedContext, ", ")))
	}
	if metadata.State == StateIncomplete {
		blockers = append(blockers, invalid("Task %q is incomplete; run status and follow recovery guidance before removal", metadata.Name))
	}

	plans := make([]taskRemovalPlan, 0, len(metadata.Attachments))
	for _, attachment := range metadata.Attachments {
		if err := config.ValidateRepositoryAlias(attachment.Alias); err != nil {
			blockers = append(blockers, invalid("Repository Attachment has invalid Repository Alias %q: %v", attachment.Alias, err))
			continue
		}
		if attachment.State == StateIncomplete {
			blockers = append(blockers, invalid("Repository Attachment %q is incomplete; run status and follow recovery guidance before removal", attachment.Alias))
		}
		worktreeInfo, err := preflightRemovalIdentity(metadata, attachment)
		if err != nil {
			blockers = append(blockers, err)
		} else {
			protected, err := protectedWorktreeContent(attachment)
			if err != nil {
				blockers = append(blockers, fmt.Errorf("inspect protected content in Task Worktree for Repository Attachment %q: %w", attachment.Alias, err))
			} else if len(protected) > 0 && !options.Force {
				blockers = append(blockers, invalid("Repository Attachment %q contains protected Task Worktree content (%s); inspect it or rerun with --force to authorize its removal", attachment.Alias, strings.Join(protected, ", ")))
			}
		}
		plan := taskRemovalPlan{attachment: attachment, worktree: worktreeInfo}
		if options.DeleteBranch {
			baseBranch, remote, fetch, err := removalBaseSettings(configuration, attachment, options.Fetch)
			if err != nil {
				blockers = append(blockers, err)
			} else {
				resolved, resolveError := gitcmd.ResolveBaseRef(attachment.MainCheckout, baseBranch, remote, fetch)
				if resolveError != nil {
					blockers = append(blockers, fmt.Errorf("resolve current Base Ref %q for Repository Attachment %q: %w", baseBranch, attachment.Alias, resolveError))
				} else {
					merged, ancestorError := gitcmd.IsAncestor(attachment.MainCheckout, "refs/heads/"+attachment.TaskBranchName, resolved.Ref)
					if ancestorError != nil {
						blockers = append(blockers, fmt.Errorf("verify Task Branch Name %q against current Base Ref %q: %w", attachment.TaskBranchName, resolved.Ref, ancestorError))
					} else if !merged && !options.Force {
						blockers = append(blockers, invalid("Task Branch Name %q for Repository Attachment %q is not fully merged into current Base Ref %q; retain it or rerun with both --delete-branch and --force", attachment.TaskBranchName, attachment.Alias, resolved.Ref))
					}
					plan.baseRef = resolved.Ref
				}
			}
		}
		plans = append(plans, plan)
	}
	if len(blockers) > 0 {
		return result, errors.Join(blockers...)
	}

	recheckedWorkspace, protectedContext, err := preflightTaskWorkspace(paths, metadata)
	if err != nil {
		return result, err
	}
	if !os.SameFile(workspaceInfo, recheckedWorkspace) {
		return result, invalid("Task Workspace %q changed identity during preflight", workspacePath)
	}
	if len(protectedContext) > 0 && !options.Force {
		return result, invalid("Task %q gained protected Task Context Files during preflight (%s)", metadata.Name, strings.Join(protectedContext, ", "))
	}
	for _, plan := range plans {
		if err := recheckRemovalIdentity(metadata, plan.attachment, plan.worktree); err != nil {
			return result, err
		}
		protected, err := protectedWorktreeContent(plan.attachment)
		if err != nil {
			return result, fmt.Errorf("recheck protected content for Repository Attachment %q: %w", plan.attachment.Alias, err)
		}
		if len(protected) > 0 && !options.Force {
			return result, invalid("Repository Attachment %q gained protected Task Worktree content during preflight (%s)", plan.attachment.Alias, strings.Join(protected, ", "))
		}
	}

	currentMetadata := originalMetadata
	for _, plan := range plans {
		repositoryResult := RemoveTaskRepositoryResult{RepositoryAlias: plan.attachment.Alias, WorktreePath: plan.attachment.WorktreePath, TaskBranchName: plan.attachment.TaskBranchName}
		if err := beforeTaskAttachmentRemovalForTest(plan.attachment.Alias); err != nil {
			return result, failTaskRemoval(metadataPath, currentMetadata, &metadata, workspacePath, err, result, plan.attachment)
		}
		if err := gitcmd.RemoveWorktree(plan.attachment.MainCheckout, plan.attachment.WorktreePath); err != nil {
			return result, failTaskRemoval(metadataPath, currentMetadata, &metadata, workspacePath, fmt.Errorf("remove Task Worktree for Repository Attachment %q: %w", plan.attachment.Alias, err), result, plan.attachment)
		}
		repositoryResult.WorktreeRemoved = true
		result.Repositories = append(result.Repositories, repositoryResult)
		if err := checkpointTaskRemoval(metadataPath, &currentMetadata, &metadata, workspacePath, result, plan.attachment, "Task Worktree removal completed; Repository Attachment cleanup is pending"); err != nil {
			return result, err
		}
		if err := afterTaskAttachmentWorktreeRemovalForTest(plan.attachment.Alias); err != nil {
			return result, failTaskRemoval(metadataPath, currentMetadata, &metadata, workspacePath, err, result, plan.attachment)
		}
		if options.DeleteBranch {
			if !options.Force {
				merged, err := gitcmd.IsAncestor(plan.attachment.MainCheckout, "refs/heads/"+plan.attachment.TaskBranchName, plan.baseRef)
				if err != nil || !merged {
					if err == nil {
						err = fmt.Errorf("Task Branch Name %q changed and is no longer fully merged into current Base Ref %q", plan.attachment.TaskBranchName, plan.baseRef)
					}
					return result, failTaskRemoval(metadataPath, currentMetadata, &metadata, workspacePath, err, result, plan.attachment)
				}
			}
			if err := gitcmd.DeleteBranch(plan.attachment.MainCheckout, plan.attachment.TaskBranchName); err != nil {
				return result, failTaskRemoval(metadataPath, currentMetadata, &metadata, workspacePath, fmt.Errorf("delete Task Branch Name %q for Repository Attachment %q: %w", plan.attachment.TaskBranchName, plan.attachment.Alias, err), result, plan.attachment)
			}
			result.Repositories[len(result.Repositories)-1].BranchDeleted = true
			if err := checkpointTaskRemoval(metadataPath, &currentMetadata, &metadata, workspacePath, result, plan.attachment, "Task Branch Name deletion completed; Repository Attachment metadata cleanup is pending"); err != nil {
				return result, err
			}
		}
		result.Repositories[len(result.Repositories)-1].Completed = true
		metadata.Attachments = removeAttachmentByAlias(metadata.Attachments, plan.attachment.Alias)
		if err := checkpointTaskRemoval(metadataPath, &currentMetadata, &metadata, workspacePath, result, RepositoryAttachment{}, "Repository Attachment removal completed; remaining Task cleanup is pending"); err != nil {
			return result, err
		}
	}

	currentWorkspace, protectedContext, err := preflightTaskWorkspace(paths, metadataWithAttachments(metadata, plans))
	if err != nil {
		return result, failTaskRemoval(metadataPath, currentMetadata, &metadata, workspacePath, err, result, RepositoryAttachment{})
	}
	if !os.SameFile(workspaceInfo, currentWorkspace) {
		return result, failTaskRemoval(metadataPath, currentMetadata, &metadata, workspacePath, invalid("Task Workspace %q changed identity during removal", workspacePath), result, RepositoryAttachment{})
	}
	if len(protectedContext) > 0 && !options.Force {
		return result, failTaskRemoval(metadataPath, currentMetadata, &metadata, workspacePath, invalid("Task %q gained protected Task Context Files during removal (%s)", metadata.Name, strings.Join(protectedContext, ", ")), result, RepositoryAttachment{})
	}
	if err := os.RemoveAll(workspacePath); err != nil {
		return result, failTaskRemoval(metadataPath, currentMetadata, &metadata, workspacePath, fmt.Errorf("remove Task Workspace: %w", err), result, RepositoryAttachment{})
	}
	if err := fileutil.SyncDirectory(paths.Workspaces); err != nil {
		return result, failTaskRemoval(metadataPath, currentMetadata, &metadata, workspacePath, fmt.Errorf("sync removed Task Workspace: %w", err), result, RepositoryAttachment{})
	}
	metadata.State = StateIncomplete
	metadata.Incomplete = taskRemovalOperation("Task Workspace removal completed; Task metadata removal is pending", result, RepositoryAttachment{}, nil)
	if err := persistRemoval(metadataPath, currentMetadata, metadata); err != nil {
		return result, fmt.Errorf("Task Workspace was removed but its checkpoint could not be persisted: %w", err)
	}
	currentMetadata, err = os.ReadFile(metadataPath)
	if err != nil {
		return result, fmt.Errorf("read final Task metadata checkpoint: %w", err)
	}
	metadataInfo, err := os.Lstat(metadataPath)
	if err != nil {
		return result, fmt.Errorf("inspect final Task metadata: %w", err)
	}
	if err := removePublishedMetadata(metadataPath, metadataInfo, currentMetadata); err != nil {
		return result, fmt.Errorf("remove Task metadata after Task Workspace removal: %w", err)
	}
	return result, nil
}

func taskRemovalLockTargets(metadata Metadata) ([]*repositoryLockTarget, error) {
	byPath := make(map[string]*repositoryLockTarget, len(metadata.Attachments))
	var failures []error
	for _, attachment := range metadata.Attachments {
		path, err := gitcmd.RepositoryLockPath(attachment.MainCheckout)
		if err != nil {
			failures = append(failures, fmt.Errorf("locate Registered Repository lock for %q: %w", attachment.Alias, err))
			continue
		}
		addRepositoryLockTarget(byPath, path, attachment.Alias)
	}
	if len(failures) > 0 {
		return nil, errors.Join(failures...)
	}
	targets := make([]*repositoryLockTarget, 0, len(byPath))
	for _, target := range byPath {
		targets = append(targets, target)
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].path < targets[j].path })
	return targets, nil
}

func preflightTaskWorkspace(paths config.Paths, metadata Metadata) (os.FileInfo, []string, error) {
	workspacePath := filepath.Join(paths.Workspaces, metadata.Name)
	root, err := filepath.EvalSymlinks(paths.Workspaces)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve Task Workspace root: %w", err)
	}
	rootInfo, err := os.Lstat(paths.Workspaces)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, nil, invalid("Task Workspace root %q is missing or no longer a real directory", paths.Workspaces)
	}
	info, err := os.Lstat(workspacePath)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, nil, invalid("Task Workspace %q is missing or no longer a real directory", workspacePath)
	}
	canonical, err := filepath.EvalSymlinks(workspacePath)
	if err != nil {
		return nil, nil, invalid("Task Workspace %q changed identity", workspacePath)
	}
	relative, err := filepath.Rel(root, canonical)
	if err != nil || relative != metadata.Name {
		return nil, nil, invalid("Task Workspace %q is not the exact managed path for Task %q", workspacePath, metadata.Name)
	}

	allowed := make(map[string]struct{}, len(metadata.ContextFiles)+len(metadata.Attachments))
	protected := make([]string, 0)
	for _, contextFile := range metadata.ContextFiles {
		if contextFile.Path == "" || contextFile.Path == "." || contextFile.Path == ".." || filepath.Base(contextFile.Path) != contextFile.Path {
			return nil, nil, invalid("recorded Task Context File %q does not name a direct Task Workspace entry", contextFile.Path)
		}
		expectedDigest, err := hex.DecodeString(contextFile.SHA256)
		if err != nil || len(expectedDigest) != sha256.Size {
			return nil, nil, invalid("recorded Task Context File %q has an invalid SHA-256 ownership digest", contextFile.Path)
		}
		allowed[contextFile.Path] = struct{}{}
		path := filepath.Join(workspacePath, contextFile.Path)
		fileInfo, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("inspect Task Context File %q: %w", path, err)
		}
		if !fileInfo.Mode().IsRegular() {
			protected = append(protected, contextFile.Path+" is not a regular file")
			continue
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, fmt.Errorf("read Task Context File %q: %w", path, err)
		}
		digest := sha256.Sum256(contents)
		if !bytes.Equal(digest[:], expectedDigest) {
			protected = append(protected, "edited "+contextFile.Path)
		}
	}
	for _, attachment := range metadata.Attachments {
		if err := config.ValidateRepositoryAlias(attachment.Alias); err != nil {
			return nil, nil, invalid("Repository Attachment has invalid Repository Alias %q: %v", attachment.Alias, err)
		}
		allowed[attachment.Alias] = struct{}{}
		path := filepath.Join(workspacePath, attachment.Alias)
		linkInfo, err := os.Lstat(path)
		if err != nil || linkInfo.Mode()&os.ModeSymlink == 0 {
			return nil, nil, invalid("Task Workspace link %q is missing or no longer a symlink", path)
		}
		expectedTarget, err := filepath.Rel(canonical, attachment.WorktreePath)
		if err != nil {
			return nil, nil, fmt.Errorf("calculate expected Task Workspace link for %q: %w", attachment.Alias, err)
		}
		target, err := os.Readlink(path)
		if err != nil || target != expectedTarget {
			return nil, nil, invalid("Task Workspace link %q does not target its recorded Task Worktree", path)
		}
	}
	entries, err := os.ReadDir(workspacePath)
	if err != nil {
		return nil, nil, fmt.Errorf("list Task Workspace: %w", err)
	}
	for _, entry := range entries {
		if _, known := allowed[entry.Name()]; !known {
			protected = append(protected, "new "+entry.Name())
		}
	}
	return info, protected, nil
}

func checkpointTaskRemoval(path string, current *[]byte, metadata *Metadata, workspacePath string, result RemoveResult, failed RepositoryAttachment, message string) error {
	metadata.State = StateIncomplete
	metadata.Incomplete = taskRemovalOperation(message, result, failed, metadata)
	if failed.Alias != "" {
		for index := range metadata.Attachments {
			if metadata.Attachments[index].Alias == failed.Alias {
				metadata.Attachments[index].State = StateIncomplete
				metadata.Attachments[index].LastError = message
				metadata.Attachments[index].ResidualObjects = observeRemovalResiduals(metadata.Attachments[index], workspacePath)
			}
		}
	}
	if err := persistRemoval(path, *current, *metadata); err != nil {
		return fmt.Errorf("persist Task removal checkpoint: %w", err)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read Task removal checkpoint: %w", err)
	}
	*current = updated
	return nil
}

func failTaskRemoval(path string, current []byte, metadata *Metadata, workspacePath string, cause error, result RemoveResult, failed RepositoryAttachment) error {
	message := cause.Error()
	if err := checkpointTaskRemoval(path, &current, metadata, workspacePath, result, failed, message); err != nil {
		return fmt.Errorf("%v; persist incomplete Task removal state: %w", cause, err)
	}
	return fmt.Errorf("%v; Task %q is incomplete; %s", cause, metadata.Name, taskRemovalSummary(result, failed, metadata))
}

func taskRemovalSummary(result RemoveResult, failed RepositoryAttachment, metadata *Metadata) string {
	completed := make([]string, 0)
	for _, repository := range result.Repositories {
		if repository.Completed {
			completed = append(completed, repository.RepositoryAlias)
		}
	}
	failedAliases := []string{}
	if failed.Alias != "" {
		failedAliases = append(failedAliases, failed.Alias)
	}
	untouched := make([]string, 0)
	for _, attachment := range metadata.Attachments {
		if attachment.Alias != failed.Alias {
			untouched = append(untouched, attachment.Alias)
		}
	}
	return fmt.Sprintf("completed Repository Attachments: %s; failed Repository Attachments: %s; untouched Repository Attachments: %s", removalAliases(completed), removalAliases(failedAliases), removalAliases(untouched))
}

func removalAliases(aliases []string) string {
	if len(aliases) == 0 {
		return "none"
	}
	return strings.Join(aliases, ", ")
}

func taskRemovalOperation(message string, result RemoveResult, failed RepositoryAttachment, metadata *Metadata) *IncompleteOperation {
	residuals := make([]string, 0)
	for _, repository := range result.Repositories {
		if repository.Completed {
			residuals = append(residuals, "completed Repository Attachment: "+repository.RepositoryAlias)
		}
	}
	if failed.Alias != "" {
		residuals = append(residuals, "failed Repository Attachment: "+failed.Alias)
	}
	if metadata != nil {
		for _, attachment := range metadata.Attachments {
			if attachment.Alias != failed.Alias {
				residuals = append(residuals, "untouched Repository Attachment: "+attachment.Alias)
			}
		}
	}
	return &IncompleteOperation{Operation: "remove_task", LastError: message, ResidualObjects: residuals, Recovery: []string{"run 'devtask status " + result.TaskName + "' and follow recovery guidance"}}
}

func removeAttachmentByAlias(attachments []RepositoryAttachment, alias string) []RepositoryAttachment {
	remaining := make([]RepositoryAttachment, 0, len(attachments)-1)
	for _, attachment := range attachments {
		if attachment.Alias != alias {
			remaining = append(remaining, attachment)
		}
	}
	return remaining
}

func metadataWithAttachments(metadata Metadata, plans []taskRemovalPlan) Metadata {
	metadata.Attachments = make([]RepositoryAttachment, 0, len(plans))
	for _, plan := range plans {
		metadata.Attachments = append(metadata.Attachments, plan.attachment)
	}
	return metadata
}
