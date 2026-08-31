package task

import (
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

type RemoveRepositoryOptions struct {
	Force        bool
	DeleteBranch bool
	Forget       bool
	Fetch        *bool
}

type RemoveRepositoryResult struct {
	TaskName         string
	RepositoryAlias  string
	WorktreePath     string
	TaskBranchName   string
	WorktreeRemoved  bool
	AttachmentForgot bool
	BranchDeleted    bool
}

type removalProgress struct {
	metadataPath     string
	originalMetadata []byte
	metadata         *Metadata
	attachmentIndex  int
	workspacePath    string
}

func RemoveRepository(paths config.Paths, configuration config.Config, taskName, requestedAlias string, options RemoveRepositoryOptions) (RemoveRepositoryResult, error) {
	if err := ValidateName(taskName); err != nil {
		return RemoveRepositoryResult{}, err
	}
	if options.Forget && options.DeleteBranch {
		return RemoveRepositoryResult{}, invalid("--forget and --delete-branch are mutually exclusive")
	}
	taskLock, err := lock.Acquire(lockPath(paths, taskName))
	if err != nil {
		if errors.Is(err, lock.ErrBusy) {
			return RemoveRepositoryResult{}, fmt.Errorf("Task %q is busy: another devtask process holds its lock", taskName)
		}
		return RemoveRepositoryResult{}, err
	}
	defer taskLock.Close()

	metadataPath, originalMetadata, metadata, err := loadForUpdate(paths, taskName)
	if err != nil {
		return RemoveRepositoryResult{}, err
	}
	attachmentIndex := -1
	for index := range metadata.Attachments {
		if strings.EqualFold(metadata.Attachments[index].Alias, requestedAlias) {
			attachmentIndex = index
			break
		}
	}
	if attachmentIndex < 0 {
		return RemoveRepositoryResult{}, invalid("Task %q has no Repository Attachment %q", metadata.Name, requestedAlias)
	}
	attachment := metadata.Attachments[attachmentIndex]
	if err := config.ValidateRepositoryAlias(attachment.Alias); err != nil {
		return RemoveRepositoryResult{}, invalid("Repository Attachment has invalid Repository Alias %q: %v", attachment.Alias, err)
	}
	result := RemoveRepositoryResult{TaskName: metadata.Name, RepositoryAlias: attachment.Alias, WorktreePath: attachment.WorktreePath, TaskBranchName: attachment.TaskBranchName}
	repositoryLockPath, err := gitcmd.RepositoryLockPath(attachment.MainCheckout)
	if err != nil {
		return result, fmt.Errorf("locate Registered Repository lock for %q: %w", attachment.Alias, err)
	}
	repositoryLock, err := lock.Acquire(repositoryLockPath)
	if err != nil {
		if errors.Is(err, lock.ErrBusy) {
			return result, fmt.Errorf("Registered Repository %q is busy: another devtask process holds its lock", attachment.Alias)
		}
		return result, err
	}
	defer repositoryLock.Close()

	remainingMetadata := append([]RepositoryAttachment(nil), metadata.Attachments[:attachmentIndex]...)
	remainingMetadata = append(remainingMetadata, metadata.Attachments[attachmentIndex+1:]...)
	remainingProjection := make([]workspace.Attachment, 0, len(remainingMetadata))
	for _, other := range remainingMetadata {
		remainingProjection = append(remainingProjection, workspace.Attachment{Alias: other.Alias, WorktreePath: other.WorktreePath})
	}
	workspacePath := filepath.Join(paths.Workspaces, metadata.Name)

	if options.Forget {
		if err := preflightForget(attachment); err != nil {
			return result, err
		}
		projection, err := workspace.PrepareRemovalProjection(workspacePath, metadata.Name, metadata.TaskBranchName, workspace.Attachment{Alias: attachment.Alias, WorktreePath: attachment.WorktreePath}, remainingProjection, true)
		if err != nil {
			return result, invalid("cannot forget Repository Attachment %q: %v", attachment.Alias, err)
		}
		if err := projection.Commit(); err != nil {
			return result, fmt.Errorf("forget Repository Attachment %q after external state was already absent: %w; inspect the Task Workspace and retry --forget", attachment.Alias, err)
		}
		metadata.Attachments = remainingMetadata
		metadata.ContextFiles = projection.RefreshOwnedContextFiles(metadata.ContextFiles)
		metadata.State = StateReady
		hasIncomplete := false
		for _, remaining := range metadata.Attachments {
			if remaining.State == StateIncomplete {
				metadata.State = StateIncomplete
				hasIncomplete = true
				break
			}
		}
		if !hasIncomplete {
			metadata.Incomplete = nil
		}
		if err := persistRemoval(metadataPath, originalMetadata, metadata); err != nil {
			return result, fmt.Errorf("forget Repository Attachment %q metadata: %w; external state remains absent, inspect the Task Workspace and retry --forget", attachment.Alias, err)
		}
		result.AttachmentForgot = true
		return result, nil
	}

	if metadata.State == StateIncomplete || attachment.State == StateIncomplete {
		return result, invalid("Repository Attachment %q is incomplete; run status and follow recovery guidance before removal, or use --forget only after both its path and Git record are absent", attachment.Alias)
	}
	worktreeInfo, err := preflightRemovalIdentity(metadata, attachment)
	if err != nil {
		return result, err
	}
	protected, err := protectedWorktreeContent(attachment)
	if err != nil {
		return result, fmt.Errorf("inspect protected content in Task Worktree for Repository Attachment %q: %w; resolve the inspection error and retry", attachment.Alias, err)
	}
	if len(protected) > 0 && !options.Force {
		return result, invalid("Repository Attachment %q contains protected Task Worktree content (%s); inspect it or rerun with --force to authorize its removal", attachment.Alias, strings.Join(protected, ", "))
	}
	projection, err := workspace.PrepareRemovalProjection(workspacePath, metadata.Name, metadata.TaskBranchName, workspace.Attachment{Alias: attachment.Alias, WorktreePath: attachment.WorktreePath}, remainingProjection, false)
	if err != nil {
		return result, invalid("cannot remove Repository Attachment %q: %v", attachment.Alias, err)
	}

	var branchPlan *branchRemovalPlan
	if options.DeleteBranch {
		prepared, err := prepareBranchRemoval(configuration, attachment, options.Fetch, options.Force)
		if err != nil {
			return result, err
		}
		branchPlan = &prepared
	}

	protected, err = protectedWorktreeContent(attachment)
	if err != nil {
		return result, fmt.Errorf("recheck protected content in Task Worktree for Repository Attachment %q: %w; resolve the inspection error and retry", attachment.Alias, err)
	}
	if len(protected) > 0 && !options.Force {
		return result, invalid("Repository Attachment %q gained protected Task Worktree content during preflight (%s); inspect it or rerun with --force to authorize its removal", attachment.Alias, strings.Join(protected, ", "))
	}
	if err := recheckRemovalIdentity(metadata, attachment, worktreeInfo); err != nil {
		return result, err
	}
	progress := &removalProgress{
		metadataPath:     metadataPath,
		originalMetadata: originalMetadata,
		metadata:         &metadata,
		attachmentIndex:  attachmentIndex,
		workspacePath:    workspacePath,
	}
	if err := gitcmd.RemoveWorktree(attachment.MainCheckout, attachment.WorktreePath); err != nil {
		return result, fmt.Errorf("remove Task Worktree for Repository Attachment %q: %w; no metadata was changed", attachment.Alias, err)
	}
	result.WorktreeRemoved = true
	if err := progress.checkpoint("Task Worktree removal completed; remaining Repository Attachment cleanup is pending"); err != nil {
		return result, fmt.Errorf("Task Worktree was removed but its incomplete checkpoint could not be persisted: %w; verify the absent path and Git record, then run 'devtask remove-repo %s %s --forget'", err, metadata.Name, attachment.Alias)
	}
	if err := afterWorktreeRemovalForTest(); err != nil {
		return result, progress.fail(err)
	}
	if options.DeleteBranch {
		if err := branchPlan.remove(attachment, options.Force); err != nil {
			return result, progress.fail(err)
		}
		result.BranchDeleted = true
		if err := progress.checkpoint("Task Branch Name deletion completed; Task Workspace cleanup is pending"); err != nil {
			return result, fmt.Errorf("Task Branch Name was deleted but its incomplete checkpoint could not be persisted: %w; verify the Task Workspace, then run 'devtask remove-repo %s %s --forget'", err, metadata.Name, attachment.Alias)
		}
	}
	if err := projection.Commit(); err != nil {
		return result, progress.fail(fmt.Errorf("update Task Workspace: %w", err))
	}
	metadata.ContextFiles = projection.RefreshOwnedContextFiles(metadata.ContextFiles)
	if err := progress.checkpoint("Task Workspace cleanup completed; Repository Attachment metadata removal is pending"); err != nil {
		return result, fmt.Errorf("Task Workspace was updated but its incomplete checkpoint could not be persisted: %w; verify the absent path and Git record, then run 'devtask remove-repo %s %s --forget'", err, metadata.Name, attachment.Alias)
	}
	metadata.Attachments = remainingMetadata
	metadata.State = StateReady
	metadata.Incomplete = nil
	if err := persistRemoval(metadataPath, progress.originalMetadata, metadata); err != nil {
		return result, fmt.Errorf("Task Worktree was removed but Repository Attachment metadata could not be updated: %w; verify the absent path and Git record, then run 'devtask remove-repo %s %s --forget'", err, metadata.Name, attachment.Alias)
	}
	return result, nil
}

type branchRemovalPlan struct {
	baseRef string
}

func prepareBranchRemoval(configuration config.Config, attachment RepositoryAttachment, fetchOverride *bool, force bool) (branchRemovalPlan, error) {
	baseBranch, remote, fetch, err := removalBaseSettings(configuration, attachment, fetchOverride)
	if err != nil {
		return branchRemovalPlan{}, err
	}
	resolved, err := gitcmd.ResolveBaseRef(attachment.MainCheckout, baseBranch, remote, fetch)
	if err != nil {
		if errors.Is(err, gitcmd.ErrBaseRefNotFound) {
			return branchRemovalPlan{}, invalid("current Base Ref %q for Repository Attachment %q does not exist: %v", baseBranch, attachment.Alias, err)
		}
		return branchRemovalPlan{}, fmt.Errorf("resolve current Base Ref %q for Repository Attachment %q: %w", baseBranch, attachment.Alias, err)
	}
	merged, err := gitcmd.IsAncestor(attachment.MainCheckout, "refs/heads/"+attachment.TaskBranchName, resolved.Ref)
	if err != nil {
		return branchRemovalPlan{}, fmt.Errorf("verify Task Branch Name %q against current Base Ref %q: %w", attachment.TaskBranchName, resolved.Ref, err)
	}
	if !merged && !force {
		return branchRemovalPlan{}, invalid("Task Branch Name %q for Repository Attachment %q is not fully merged into current Base Ref %q; retain it or rerun with both --delete-branch and --force", attachment.TaskBranchName, attachment.Alias, resolved.Ref)
	}
	return branchRemovalPlan{baseRef: resolved.Ref}, nil
}

func (plan branchRemovalPlan) remove(attachment RepositoryAttachment, force bool) error {
	if !force {
		merged, err := gitcmd.IsAncestor(attachment.MainCheckout, "refs/heads/"+attachment.TaskBranchName, plan.baseRef)
		if err != nil {
			return fmt.Errorf("recheck Task Branch Name %q against current Base Ref %q: %w", attachment.TaskBranchName, plan.baseRef, err)
		}
		if !merged {
			return fmt.Errorf("Task Branch Name %q changed and is no longer fully merged into current Base Ref %q; branch was retained", attachment.TaskBranchName, plan.baseRef)
		}
	}
	if err := gitcmd.DeleteBranch(attachment.MainCheckout, attachment.TaskBranchName); err != nil {
		return fmt.Errorf("delete Task Branch Name %q: %w", attachment.TaskBranchName, err)
	}
	return nil
}

func preflightForget(attachment RepositoryAttachment) error {
	if _, err := os.Lstat(attachment.WorktreePath); err == nil {
		return invalid("--forget requires the recorded Task Worktree path %q to be absent", attachment.WorktreePath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect Task Worktree path before --forget: %w", err)
	}
	if record, err := gitcmd.WorktreeAt(attachment.MainCheckout, attachment.WorktreePath); err == nil {
		return invalid("--forget requires the Git worktree record for %q to be absent; found %q", attachment.WorktreePath, record.Path)
	} else if !errors.Is(err, gitcmd.ErrWorktreeRecordNotFound) {
		return fmt.Errorf("inspect Git worktree record before --forget: %w", err)
	}
	return nil
}

func preflightRemovalIdentity(metadata Metadata, attachment RepositoryAttachment) (os.FileInfo, error) {
	canonicalMain, err := repo.ResolveMainCheckout(attachment.MainCheckout)
	if err != nil || canonicalMain != attachment.MainCheckout {
		return nil, invalid("Repository Attachment %q Main Checkout identity is missing or changed", attachment.Alias)
	}
	if filepath.Clean(attachment.WorktreePath) == canonicalMain {
		return nil, invalid("refuse to remove Main Checkout %q", canonicalMain)
	}
	expectedPath, err := containedWorktreePath(canonicalMain, metadata.Name)
	if err != nil || expectedPath != attachment.WorktreePath {
		return nil, invalid("Repository Attachment %q Task Worktree path %q is not the exact managed path %q", attachment.Alias, attachment.WorktreePath, expectedPath)
	}
	root := filepath.Dir(expectedPath)
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, invalid("Repository Attachment %q worktrees path %q is not a real directory", attachment.Alias, root)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil || canonicalRoot != root {
		return nil, invalid("Repository Attachment %q worktrees path %q changed containment identity", attachment.Alias, root)
	}
	info, err := os.Lstat(attachment.WorktreePath)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, invalid("Repository Attachment %q Task Worktree path is missing or no longer a real directory", attachment.Alias)
	}
	canonicalWorktree, err := filepath.EvalSymlinks(attachment.WorktreePath)
	if err != nil || canonicalWorktree != attachment.WorktreePath {
		return nil, invalid("Repository Attachment %q Task Worktree path changed identity", attachment.Alias)
	}
	mainInfo, err := os.Lstat(canonicalMain)
	if err != nil {
		return nil, fmt.Errorf("inspect Main Checkout identity: %w", err)
	}
	if os.SameFile(mainInfo, info) || canonicalMain == canonicalWorktree {
		return nil, invalid("refuse to remove Main Checkout %q", canonicalMain)
	}
	resolvedMain, err := repo.ResolveMainCheckout(attachment.WorktreePath)
	if err != nil || resolvedMain != canonicalMain {
		return nil, invalid("Repository Attachment %q Task Worktree does not belong to its recorded Main Checkout", attachment.Alias)
	}
	record, err := gitcmd.WorktreeAt(canonicalMain, attachment.WorktreePath)
	if err != nil || record.Prunable || record.Path != attachment.WorktreePath || record.BranchRef != "refs/heads/"+attachment.TaskBranchName {
		return nil, invalid("Repository Attachment %q Git worktree record does not prove the expected path and Task Branch Name", attachment.Alias)
	}
	branch, err := gitcmd.Run(attachment.WorktreePath, "branch", "--show-current")
	if err != nil || strings.TrimSpace(string(branch)) != attachment.TaskBranchName {
		return nil, invalid("Repository Attachment %q Task Worktree is not on Task Branch Name %q", attachment.Alias, attachment.TaskBranchName)
	}
	return info, nil
}

func recheckRemovalIdentity(metadata Metadata, attachment RepositoryAttachment, expected os.FileInfo) error {
	current, err := preflightRemovalIdentity(metadata, attachment)
	if err != nil {
		return err
	}
	if !os.SameFile(expected, current) {
		return invalid("Repository Attachment %q Task Worktree changed identity during removal", attachment.Alias)
	}
	return nil
}

func protectedWorktreeContent(attachment RepositoryAttachment) ([]string, error) {
	status, err := gitcmd.Status(attachment.WorktreePath)
	if err != nil {
		return nil, fmt.Errorf("inspect Git status: %w", err)
	}
	protected := make([]string, 0, 5)
	if status.Modified {
		protected = append(protected, "modified")
	}
	if status.Staged {
		protected = append(protected, "staged")
	}
	if status.Untracked {
		protected = append(protected, "untracked")
	}
	if status.Conflicted {
		protected = append(protected, "conflicted")
	}
	managed := make(map[string]struct{}, len(attachment.ManagedLinks))
	for _, link := range attachment.ManagedLinks {
		relative, err := filepath.Rel(attachment.WorktreePath, link.Destination)
		if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("recorded Shared Local Path destination %q escapes the Task Worktree", link.Destination)
		}
		info, err := os.Lstat(link.Destination)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			protected = append(protected, "changed Shared Local Path "+relative)
			continue
		}
		target, err := os.Readlink(link.Destination)
		if err != nil || target != link.Target {
			protected = append(protected, "changed Shared Local Path "+relative)
			continue
		}
		managed[filepath.ToSlash(relative)] = struct{}{}
	}
	ignored, err := gitcmd.IgnoredPaths(attachment.WorktreePath)
	if err != nil {
		return nil, fmt.Errorf("inspect ignored content: %w", err)
	}
	for _, path := range ignored {
		if _, known := managed[filepath.ToSlash(path)]; !known {
			protected = append(protected, "unknown ignored "+path)
		}
	}
	return protected, nil
}

func removalBaseSettings(configuration config.Config, attachment RepositoryAttachment, fetchOverride *bool) (string, string, bool, error) {
	baseBranch := attachment.BaseBranch
	remote := configuration.Defaults.Remote
	fetch := configuration.Defaults.Fetch
	for alias, repositoryConfiguration := range configuration.Repositories {
		if !strings.EqualFold(alias, attachment.Alias) {
			continue
		}
		if repositoryConfiguration.Remote != "" {
			remote = repositoryConfiguration.Remote
		}
		if repositoryConfiguration.Fetch != nil {
			fetch = *repositoryConfiguration.Fetch
		}
		break
	}
	if baseBranch == "" {
		baseBranch = configuration.Defaults.BaseBranch
	}
	if fetchOverride != nil {
		fetch = *fetchOverride
	}
	if strings.TrimSpace(baseBranch) == "" {
		return "", "", false, invalid("current Base Ref for Repository Attachment %q is empty", attachment.Alias)
	}
	if err := gitcmd.ValidateBranchName(baseBranch); err != nil {
		return "", "", false, invalid("invalid current Base Ref for Repository Attachment %q: %v", attachment.Alias, err)
	}
	return baseBranch, remote, fetch, nil
}

func persistRemoval(path string, original []byte, metadata Metadata) error {
	updated, err := yaml.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("encode Task metadata: %w", err)
	}
	outcome, err := fileutil.WriteAtomicIfUnchanged(path, original, updated, 0o600)
	if err != nil {
		if outcome.Published {
			return fmt.Errorf("metadata was published but not durably synced: %w", err)
		}
		return fmt.Errorf("update Task metadata: %w", err)
	}
	return nil
}

func (progress *removalProgress) fail(cause error) error {
	if err := progress.checkpoint(cause.Error()); err != nil {
		return fmt.Errorf("%v; persist incomplete removal state: %w", cause, err)
	}
	return fmt.Errorf("%v; Task %q is incomplete; %s", cause, progress.metadata.Name, progress.metadata.Incomplete.Recovery[0])
}

func (progress *removalProgress) checkpoint(message string) error {
	attachment := &progress.metadata.Attachments[progress.attachmentIndex]
	residuals := observeRemovalResiduals(*attachment, progress.workspacePath)
	recovery := []string{fmt.Sprintf("verify the absent Task Worktree path and Git record, then run 'devtask remove-repo %s %s --forget'", progress.metadata.Name, attachment.Alias)}
	progress.metadata.State = StateIncomplete
	attachment.State = StateIncomplete
	attachment.LastError = message
	attachment.ResidualObjects = append([]string(nil), residuals...)
	progress.metadata.Incomplete = &IncompleteOperation{
		Operation:       "remove_repository",
		LastError:       message,
		ResidualObjects: append([]string(nil), residuals...),
		Recovery:        recovery,
	}
	updated, err := yaml.Marshal(progress.metadata)
	if err != nil {
		return fmt.Errorf("encode incomplete removal checkpoint: %w", err)
	}
	outcome, err := fileutil.WriteAtomicIfUnchanged(progress.metadataPath, progress.originalMetadata, updated, 0o600)
	if err != nil {
		if outcome.Published {
			progress.originalMetadata = updated
			return fmt.Errorf("checkpoint was published but not durably synced: %w", err)
		}
		return fmt.Errorf("update incomplete removal checkpoint: %w", err)
	}
	progress.originalMetadata = updated
	return nil
}

func observeRemovalResiduals(attachment RepositoryAttachment, workspacePath string) []string {
	residuals := make([]string, 0, 6)
	if _, err := os.Lstat(attachment.WorktreePath); errors.Is(err, os.ErrNotExist) {
		residuals = append(residuals, "Task Worktree path is absent: "+attachment.WorktreePath)
	} else if err == nil {
		residuals = append(residuals, "Task Worktree path remains: "+attachment.WorktreePath)
	} else {
		residuals = append(residuals, "Task Worktree path inspection failed: "+err.Error())
	}
	if record, err := gitcmd.WorktreeAt(attachment.MainCheckout, attachment.WorktreePath); errors.Is(err, gitcmd.ErrWorktreeRecordNotFound) {
		residuals = append(residuals, "Git worktree record is absent: "+attachment.WorktreePath)
	} else if err == nil {
		residuals = append(residuals, "Git worktree record remains: "+record.Path)
	} else {
		residuals = append(residuals, "Git worktree record inspection failed: "+err.Error())
	}
	if exists, err := gitcmd.RefExists(attachment.MainCheckout, "refs/heads/"+attachment.TaskBranchName); err != nil {
		residuals = append(residuals, "Task Branch Name inspection failed: "+err.Error())
	} else if exists {
		residuals = append(residuals, "Task Branch Name remains: "+attachment.TaskBranchName)
	} else {
		residuals = append(residuals, "Task Branch Name is absent: "+attachment.TaskBranchName)
	}
	workspaceLink := filepath.Join(workspacePath, attachment.Alias)
	if _, err := os.Lstat(workspaceLink); errors.Is(err, os.ErrNotExist) {
		residuals = append(residuals, "Task Workspace link is absent: "+workspaceLink)
	} else if err == nil {
		residuals = append(residuals, "Task Workspace link remains: "+workspaceLink)
	} else {
		residuals = append(residuals, "Task Workspace link inspection failed: "+err.Error())
	}
	agentsPath := filepath.Join(workspacePath, "AGENTS.md")
	if contents, err := os.ReadFile(agentsPath); err != nil {
		residuals = append(residuals, "generated AGENTS entry inspection failed: "+err.Error())
	} else if strings.Contains(string(contents), fmt.Sprintf("- `%s`: `%s`", attachment.Alias, attachment.WorktreePath)) {
		residuals = append(residuals, "generated AGENTS entry remains: "+agentsPath)
	} else {
		residuals = append(residuals, "generated AGENTS entry is absent: "+agentsPath)
	}
	return residuals
}
