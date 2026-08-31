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

type AddResult struct {
	TaskName        string
	Attachment      RepositoryAttachment
	AlreadyAttached bool
}

func AddRepository(paths config.Paths, configuration config.Config, taskName, repositoryAlias string, baseOverride *string, fetchOverride *bool) (AddResult, error) {
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
	if metadata.State == StateIncomplete {
		return AddResult{}, invalid("Task %q is incomplete; run status and follow recovery guidance before adding repositories", metadata.Name)
	}
	for _, attachment := range metadata.Attachments {
		if !strings.EqualFold(attachment.Alias, repositoryAlias) {
			continue
		}
		if err := verifyExistingAttachment(metadata, attachment, paths); err != nil {
			return AddResult{}, err
		}
		return AddResult{TaskName: metadata.Name, Attachment: attachment, AlreadyAttached: true}, nil
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
	branchRef := "refs/heads/" + metadata.TaskBranchName
	branchExisted, err := gitcmd.RefExists(mainCheckout, branchRef)
	if err != nil {
		return AddResult{}, fmt.Errorf("inspect Task Branch Name %q in repository %q: %w", metadata.TaskBranchName, alias, err)
	}
	if branchExisted {
		if owner, err := gitcmd.WorktreeForBranch(mainCheckout, branchRef); err == nil {
			detail := owner.Path
			if owner.Prunable {
				detail += " (prunable)"
			}
			return AddResult{}, invalid("Task Branch Name %q is already assigned to Git worktree %q; refusing to prune or steal it", metadata.TaskBranchName, detail)
		} else if !errors.Is(err, gitcmd.ErrWorktreeRecordNotFound) {
			return AddResult{}, fmt.Errorf("inspect Task Branch Name ownership in repository %q: %w", alias, err)
		}
	}
	baseRef := ""
	baseCommit := ""
	if !branchExisted {
		remote := configuration.Defaults.Remote
		if repositoryConfiguration.Remote != "" {
			remote = repositoryConfiguration.Remote
		}
		fetch := configuration.Defaults.Fetch
		if repositoryConfiguration.Fetch != nil {
			fetch = *repositoryConfiguration.Fetch
		}
		if fetchOverride != nil {
			fetch = *fetchOverride
		}
		resolvedBase, err := gitcmd.ResolveBase(mainCheckout, baseBranch, remote, fetch)
		if err != nil {
			if errors.Is(err, gitcmd.ErrBaseRefNotFound) {
				return AddResult{}, invalid("Base Ref %q for repository %q does not exist: %v", baseBranch, alias, err)
			}
			return AddResult{}, fmt.Errorf("resolve Base Ref %q for repository %q: %w", baseBranch, alias, err)
		}
		baseRef = resolvedBase.Ref
		baseCommit = resolvedBase.Commit
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
	if record, err := gitcmd.WorktreeAt(mainCheckout, expectedWorktree); err == nil {
		detail := record.BranchRef
		if record.Prunable {
			detail += " (prunable)"
		}
		return AddResult{}, invalid("Git worktree record collision at %q: %s", expectedWorktree, strings.TrimSpace(detail))
	} else if !errors.Is(err, gitcmd.ErrWorktreeRecordNotFound) {
		return AddResult{}, fmt.Errorf("inspect Git worktree ownership for %q: %w", expectedWorktree, err)
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
	attachment := RepositoryAttachment{
		Alias:          alias,
		MainCheckout:   mainCheckout,
		WorktreePath:   expectedWorktree,
		TaskBranchName: metadata.TaskBranchName,
		BaseBranch:     baseBranch,
		BaseRef:        baseRef,
		BaseCommit:     baseCommit,
		Order:          len(metadata.Attachments),
		BranchExisted:  branchExisted,
		ManagedLinks:   make([]workspace.ManagedLink, 0),
		State:          StateReady,
	}

	excludeChange, err := gitcmd.EnsureWorktreesIgnored(mainCheckout)
	if err != nil {
		return AddResult{}, err
	}
	rollback := func(cause error, cleanGitObjects bool) error {
		var rollbackErrors []error
		rollbackErrors = append(rollbackErrors, projection.Abort())
		if cleanGitObjects {
			if _, recordError := gitcmd.WorktreeAt(mainCheckout, expectedWorktree); recordError == nil {
				if removeError := gitcmd.RemoveWorktree(mainCheckout, expectedWorktree); removeError != nil {
					rollbackErrors = append(rollbackErrors, fmt.Errorf("remove Task Worktree: %w", removeError))
				}
			} else if errors.Is(recordError, gitcmd.ErrWorktreeRecordNotFound) {
				if _, pathError := os.Lstat(expectedWorktree); pathError == nil {
					rollbackErrors = append(rollbackErrors, fmt.Errorf("unregistered Task Worktree path remains at %q; refuse automatic removal", expectedWorktree))
				} else if !errors.Is(pathError, os.ErrNotExist) {
					rollbackErrors = append(rollbackErrors, fmt.Errorf("inspect failed Task Worktree path: %w", pathError))
				}
			} else {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("inspect Task Worktree record during rollback: %w", recordError))
			}
			if exists, branchError := gitcmd.RefExists(mainCheckout, branchRef); branchError != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("inspect Task Branch Name during rollback: %w", branchError))
			} else if exists && !branchExisted {
				if deleteError := gitcmd.DeleteBranch(mainCheckout, metadata.TaskBranchName); deleteError != nil {
					rollbackErrors = append(rollbackErrors, fmt.Errorf("delete Task Branch Name: %w", deleteError))
				}
			}
		}
		if rootCreated {
			if removeError := os.Remove(worktreesRoot); removeError != nil && !errors.Is(removeError, os.ErrNotExist) {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("remove empty worktrees directory: %w", removeError))
			}
		}
		rollbackErrors = append(rollbackErrors, excludeChange.Abort())
		if joined := errors.Join(rollbackErrors...); joined != nil {
			residuals := observeResidualObjects(metadata.Name, attachment, paths, joined)
			if persistError := persistIncompleteAttachment(metadataPath, attachment, cause, joined, residuals); persistError != nil {
				return fmt.Errorf("%v; roll back Repository Attachment: %v; persist incomplete state: %w", cause, joined, persistError)
			}
			return fmt.Errorf("%v; roll back Repository Attachment: %v; Task %q is incomplete with residual state; run status and follow recovery guidance", cause, joined, metadata.Name)
		}
		return cause
	}

	createWorktree := func() error {
		if branchExisted {
			return gitcmd.AttachWorktree(mainCheckout, expectedWorktree, metadata.TaskBranchName)
		}
		return gitcmd.CreateWorktree(mainCheckout, expectedWorktree, metadata.TaskBranchName, baseRef)
	}
	if err := createWorktree(); err != nil {
		return AddResult{}, rollback(fmt.Errorf("create Task Worktree for repository %q: %w", alias, err), true)
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
	if err := afterProjectionForTest(); err != nil {
		return AddResult{}, rollback(err, true)
	}
	attachment.WorktreePath = canonicalWorktree
	metadata.Attachments = append(metadata.Attachments, attachment)
	metadata.ContextFiles = projection.RefreshOwnedContextFiles(metadata.ContextFiles)
	updatedMetadata, err := yaml.Marshal(metadata)
	if err != nil {
		return AddResult{}, rollback(fmt.Errorf("encode Task metadata: %w", err), true)
	}
	outcome, err := fileutil.WriteAtomicIfUnchanged(metadataPath, originalMetadata, updatedMetadata, 0o600)
	if err != nil {
		if outcome.Published {
			return AddResult{}, fmt.Errorf("Repository Attachment for %q was published but its Task metadata could not be durably synced: %w; inspect the Task before retrying", alias, err)
		}
		return AddResult{}, rollback(fmt.Errorf("update Task metadata: %w", err), true)
	}
	return AddResult{TaskName: metadata.Name, Attachment: attachment}, nil
}

func observeResidualObjects(taskName string, attachment RepositoryAttachment, paths config.Paths, rollbackError error) []string {
	residuals := []string{"rollback error: " + rollbackError.Error()}
	if _, err := os.Lstat(attachment.WorktreePath); err == nil {
		residuals = append(residuals, "Task Worktree path remains: "+attachment.WorktreePath)
	}
	if record, err := gitcmd.WorktreeAt(attachment.MainCheckout, attachment.WorktreePath); err == nil {
		residuals = append(residuals, "Git worktree record remains: "+record.Path)
	}
	if exists, err := gitcmd.RefExists(attachment.MainCheckout, "refs/heads/"+attachment.TaskBranchName); err == nil && exists {
		residuals = append(residuals, "Task Branch Name remains: "+attachment.TaskBranchName)
	}
	linkPath := filepath.Join(paths.Workspaces, taskName, attachment.Alias)
	if _, err := os.Lstat(linkPath); err == nil {
		residuals = append(residuals, "Task Workspace entry remains: "+linkPath)
	}
	return residuals
}

func persistIncompleteAttachment(metadataPath string, attachment RepositoryAttachment, cause, rollbackError error, residuals []string) error {
	current, err := os.ReadFile(metadataPath)
	if err != nil {
		return err
	}
	metadata, err := load(metadataPath)
	if err != nil {
		return err
	}
	attachment.State = StateIncomplete
	attachment.LastError = cause.Error()
	attachment.ResidualObjects = residuals
	metadata.State = StateIncomplete
	metadata.Attachments = append(metadata.Attachments, attachment)
	metadata.Incomplete = &IncompleteOperation{
		Operation:       "add_repository",
		LastError:       cause.Error() + "; rollback: " + rollbackError.Error(),
		ResidualObjects: residuals,
		Recovery: []string{
			"inspect each residual object before changing it",
			"restore or remove changed Task Workspace entries, then retry recovery",
		},
	}
	updated, err := yaml.Marshal(metadata)
	if err != nil {
		return err
	}
	outcome, err := fileutil.WriteAtomicIfUnchanged(metadataPath, current, updated, 0o600)
	if err != nil {
		if outcome.Published {
			return fmt.Errorf("incomplete state was published but not durably synced: %w", err)
		}
		return err
	}
	return nil
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

func verifyExistingAttachment(metadata Metadata, attachment RepositoryAttachment, paths config.Paths) error {
	if attachment.TaskBranchName != metadata.TaskBranchName {
		return invalid("Repository Attachment %q does not match its recorded ownership; run status for recovery guidance", attachment.Alias)
	}
	canonicalMainCheckout, err := repo.ResolveMainCheckout(attachment.MainCheckout)
	if err != nil || canonicalMainCheckout != attachment.MainCheckout {
		return invalid("Repository Attachment %q Main Checkout is missing or changed", attachment.Alias)
	}
	canonical, err := filepath.EvalSymlinks(attachment.WorktreePath)
	if err != nil || canonical != attachment.WorktreePath {
		return invalid("Repository Attachment %q Task Worktree is missing or changed", attachment.Alias)
	}
	record, err := gitcmd.WorktreeAt(attachment.MainCheckout, attachment.WorktreePath)
	if err != nil || record.BranchRef != "refs/heads/"+metadata.TaskBranchName {
		return invalid("Repository Attachment %q Git worktree ownership is missing or changed", attachment.Alias)
	}
	branch, err := gitcmd.Run(attachment.WorktreePath, "branch", "--show-current")
	if err != nil || strings.TrimSpace(string(branch)) != metadata.TaskBranchName {
		return invalid("Repository Attachment %q is not on Task Branch Name %q", attachment.Alias, metadata.TaskBranchName)
	}
	linkPath := filepath.Join(paths.Workspaces, metadata.Name, attachment.Alias)
	resolved, err := filepath.EvalSymlinks(linkPath)
	if err != nil || resolved != attachment.WorktreePath {
		return invalid("Repository Attachment %q Task Workspace link is missing or changed", attachment.Alias)
	}
	return nil
}
