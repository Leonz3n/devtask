package task

import (
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
	results, err := AddRepositories(paths, configuration, taskName, []string{repositoryAlias}, baseOverride, fetchOverride)
	if err != nil {
		return AddResult{}, err
	}
	return results[0], nil
}

type addRequest struct {
	existingIndex int
	planIndex     int
}

type addPlan struct {
	repositoryConfiguration config.RepositoryConfig
	attachment              RepositoryAttachment
	branchRef               string
	branchExisted           bool
	branchOwned             bool
	worktreesRootWasAbsent  bool
	worktreesRootInfo       os.FileInfo
	excludeChange           *gitcmd.ExcludeUpdate
	worktreeAttempted       bool
	worktreeOwned           bool
	ownedWorktreePath       string
	ownedWorktreeInfo       os.FileInfo
	compensationErrors      []error
}

type repositoryLockTarget struct {
	path    string
	aliases []string
	lock    *lock.File
}

func AddRepositories(paths config.Paths, configuration config.Config, taskName string, repositoryAliases []string, baseOverride *string, fetchOverride *bool) ([]AddResult, error) {
	if err := ValidateName(taskName); err != nil {
		return nil, err
	}
	if len(repositoryAliases) == 0 {
		return nil, invalid("at least one Repository Alias is required")
	}
	taskLock, err := lock.Acquire(lockPath(paths, taskName))
	if err != nil {
		if errors.Is(err, lock.ErrBusy) {
			return nil, fmt.Errorf("Task %q is busy: another devtask process holds its lock", taskName)
		}
		return nil, err
	}
	defer taskLock.Close()

	metadataPath, originalMetadata, metadata, err := loadForUpdate(paths, taskName)
	if err != nil {
		return nil, err
	}
	if metadata.State == StateIncomplete {
		return nil, invalid("Task %q is incomplete; run status and follow recovery guidance before adding repositories", metadata.Name)
	}

	requests := make([]addRequest, 0, len(repositoryAliases))
	plans := make([]addPlan, 0, len(repositoryAliases))
	lockTargetsByPath := make(map[string]*repositoryLockTarget)
	seenAliases := make(map[string]string, len(repositoryAliases))
	seenWorktrees := make(map[string]string, len(repositoryAliases))
	for _, requestedAlias := range repositoryAliases {
		foldedAlias := strings.ToLower(requestedAlias)
		if previous, duplicate := seenAliases[foldedAlias]; duplicate {
			return nil, invalid("Repository Alias %q is requested more than once (as %q and %q)", requestedAlias, previous, requestedAlias)
		}
		seenAliases[foldedAlias] = requestedAlias
		existingIndex := -1
		for index := range metadata.Attachments {
			if strings.EqualFold(metadata.Attachments[index].Alias, requestedAlias) {
				existingIndex = index
				break
			}
		}
		if existingIndex >= 0 {
			attachment := metadata.Attachments[existingIndex]
			repositoryLockPath, err := gitcmd.RepositoryLockPath(attachment.MainCheckout)
			if err != nil {
				return nil, fmt.Errorf("locate Registered Repository lock for %q: %w", attachment.Alias, err)
			}
			addRepositoryLockTarget(lockTargetsByPath, repositoryLockPath, attachment.Alias)
			requests = append(requests, addRequest{existingIndex: existingIndex, planIndex: -1})
			continue
		}
		alias, repositoryConfiguration, err := findRegisteredRepository(configuration, requestedAlias)
		if err != nil {
			return nil, err
		}
		mainCheckout, err := repo.ResolveMainCheckout(repositoryConfiguration.Path)
		if err != nil {
			return nil, fmt.Errorf("inspect Registered Repository %q: %w", alias, err)
		}
		expectedWorktree, err := containedWorktreePath(mainCheckout, metadata.Name)
		if err != nil {
			return nil, err
		}
		if previousAlias, duplicate := seenWorktrees[expectedWorktree]; duplicate {
			return nil, invalid("Registered Repositories %q and %q resolve to the same Task Worktree %q", previousAlias, alias, expectedWorktree)
		}
		seenWorktrees[expectedWorktree] = alias
		repositoryLockPath, err := gitcmd.RepositoryLockPath(mainCheckout)
		if err != nil {
			return nil, fmt.Errorf("locate Registered Repository lock for %q: %w", alias, err)
		}
		addRepositoryLockTarget(lockTargetsByPath, repositoryLockPath, alias)
		baseBranch := configuration.Defaults.BaseBranch
		if repositoryConfiguration.BaseBranch != "" {
			baseBranch = repositoryConfiguration.BaseBranch
		}
		if baseOverride != nil {
			baseBranch = *baseOverride
		}
		planIndex := len(plans)
		plans = append(plans, addPlan{
			repositoryConfiguration: repositoryConfiguration,
			attachment: RepositoryAttachment{
				Alias:          alias,
				MainCheckout:   mainCheckout,
				WorktreePath:   expectedWorktree,
				TaskBranchName: metadata.TaskBranchName,
				BaseBranch:     baseBranch,
				Order:          len(metadata.Attachments) + planIndex,
				ManagedLinks:   make([]workspace.ManagedLink, 0),
				State:          StateReady,
			},
			branchRef: "refs/heads/" + metadata.TaskBranchName,
		})
		requests = append(requests, addRequest{existingIndex: -1, planIndex: planIndex})
	}

	lockTargets := make([]*repositoryLockTarget, 0, len(lockTargetsByPath))
	for _, target := range lockTargetsByPath {
		lockTargets = append(lockTargets, target)
	}
	sort.Slice(lockTargets, func(i, j int) bool { return lockTargets[i].path < lockTargets[j].path })
	for _, target := range lockTargets {
		target.lock, err = lock.Acquire(target.path)
		if err != nil {
			closeRepositoryLocks(lockTargets)
			if errors.Is(err, lock.ErrBusy) {
				return nil, fmt.Errorf("Registered Repository %q is busy: another devtask process holds its lock", strings.Join(target.aliases, ", "))
			}
			return nil, err
		}
	}
	defer closeRepositoryLocks(lockTargets)
	lockPaths := make([]string, len(lockTargets))
	for index, target := range lockTargets {
		lockPaths[index] = target.path
	}
	if err := afterRepositoryLocksForTest(lockPaths); err != nil {
		return nil, fmt.Errorf("record Registered Repository lock acquisition: %w", err)
	}

	for _, request := range requests {
		if request.existingIndex >= 0 {
			if err := verifyExistingAttachment(metadata, metadata.Attachments[request.existingIndex], paths); err != nil {
				return nil, err
			}
		}
	}
	if len(plans) == 0 {
		results := make([]AddResult, 0, len(requests))
		for _, request := range requests {
			results = append(results, AddResult{TaskName: metadata.Name, Attachment: metadata.Attachments[request.existingIndex], AlreadyAttached: true})
		}
		return results, nil
	}
	for index := range plans {
		plan := &plans[index]
		plan.branchExisted, err = gitcmd.RefExists(plan.attachment.MainCheckout, plan.branchRef)
		if err != nil {
			return nil, fmt.Errorf("inspect Task Branch Name %q in repository %q: %w", metadata.TaskBranchName, plan.attachment.Alias, err)
		}
		plan.attachment.BranchExisted = plan.branchExisted
		if plan.branchExisted {
			if owner, ownerError := gitcmd.WorktreeForBranch(plan.attachment.MainCheckout, plan.branchRef); ownerError == nil {
				detail := owner.Path
				if owner.Prunable {
					detail += " (prunable)"
				}
				return nil, invalid("Task Branch Name %q is already assigned to Git worktree %q; refusing to prune or steal it", metadata.TaskBranchName, detail)
			} else if !errors.Is(ownerError, gitcmd.ErrWorktreeRecordNotFound) {
				return nil, fmt.Errorf("inspect Task Branch Name ownership in repository %q: %w", plan.attachment.Alias, ownerError)
			}
		}
		plan.worktreesRootWasAbsent, err = preflightWorktreeRoot(filepath.Dir(plan.attachment.WorktreePath))
		if err != nil {
			return nil, err
		}
		if _, pathError := os.Lstat(plan.attachment.WorktreePath); pathError == nil {
			return nil, invalid("Task Worktree collision at %q", plan.attachment.WorktreePath)
		} else if !errors.Is(pathError, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect Task Worktree path %q: %w", plan.attachment.WorktreePath, pathError)
		}
		if record, recordError := gitcmd.WorktreeAt(plan.attachment.MainCheckout, plan.attachment.WorktreePath); recordError == nil {
			detail := record.BranchRef
			if record.Prunable {
				detail += " (prunable)"
			}
			return nil, invalid("Git worktree record collision at %q: %s", plan.attachment.WorktreePath, strings.TrimSpace(detail))
		} else if !errors.Is(recordError, gitcmd.ErrWorktreeRecordNotFound) {
			return nil, fmt.Errorf("inspect Git worktree ownership for %q: %w", plan.attachment.WorktreePath, recordError)
		}
	}

	workspacePath := filepath.Join(paths.Workspaces, metadata.Name)
	projectionAdditions := make([]workspace.Attachment, 0, len(plans))
	projectionAttachments := make([]workspace.Attachment, 0, len(metadata.Attachments)+len(plans))
	for _, attachment := range metadata.Attachments {
		projectionAttachments = append(projectionAttachments, workspace.Attachment{Alias: attachment.Alias, WorktreePath: attachment.WorktreePath})
	}
	for _, plan := range plans {
		addition := workspace.Attachment{Alias: plan.attachment.Alias, WorktreePath: plan.attachment.WorktreePath}
		projectionAdditions = append(projectionAdditions, addition)
		projectionAttachments = append(projectionAttachments, addition)
	}
	var projection *workspace.Projection
	if len(plans) > 0 {
		projection, err = workspace.PrepareProjectionBatch(workspacePath, metadata.Name, metadata.TaskBranchName, projectionAdditions, projectionAttachments)
		if err != nil {
			if errors.Is(err, workspace.ErrCollision) {
				return nil, invalid("%v", err)
			}
			return nil, err
		}
	}

	for index := range plans {
		plan := &plans[index]
		if plan.branchExisted {
			continue
		}
		if strings.TrimSpace(plan.attachment.BaseBranch) == "" {
			return nil, invalid("Base Ref for repository %q must be a non-empty branch name", plan.attachment.Alias)
		}
		if err := gitcmd.ValidateBranchName(plan.attachment.BaseBranch); err != nil {
			return nil, invalid("invalid Base Ref for repository %q: %v", plan.attachment.Alias, err)
		}
	}
	for index := range plans {
		plan := &plans[index]
		if plan.branchExisted {
			continue
		}
		remote := configuration.Defaults.Remote
		if plan.repositoryConfiguration.Remote != "" {
			remote = plan.repositoryConfiguration.Remote
		}
		fetch := configuration.Defaults.Fetch
		if plan.repositoryConfiguration.Fetch != nil {
			fetch = *plan.repositoryConfiguration.Fetch
		}
		if fetchOverride != nil {
			fetch = *fetchOverride
		}
		resolvedBaseRef, resolveError := gitcmd.ResolveBaseRef(plan.attachment.MainCheckout, plan.attachment.BaseBranch, remote, fetch)
		if resolveError != nil {
			if errors.Is(resolveError, gitcmd.ErrBaseRefNotFound) {
				return nil, invalid("Base Ref %q for repository %q does not exist: %v", plan.attachment.BaseBranch, plan.attachment.Alias, resolveError)
			}
			return nil, fmt.Errorf("resolve Base Ref %q for repository %q: %w", plan.attachment.BaseBranch, plan.attachment.Alias, resolveError)
		}
		plan.attachment.BaseRef = resolvedBaseRef.Ref
		plan.attachment.BaseCommit = resolvedBaseRef.Commit
	}
	for index := range plans {
		plans[index].excludeChange, err = gitcmd.PrepareWorktreesIgnored(plans[index].attachment.MainCheckout)
		if err != nil {
			return nil, fmt.Errorf("prepare Task Worktree for repository %q: %w", plans[index].attachment.Alias, err)
		}
	}

	rollback := func(cause error) error {
		var rollbackErrors []error
		if projection != nil {
			rollbackErrors = append(rollbackErrors, projection.Abort())
		}
		for index := len(plans) - 1; index >= 0; index-- {
			plan := &plans[index]
			beforeCompensationForTest(plan.attachment.Alias)
			rollbackErrors = append(rollbackErrors, plan.compensationErrors...)
			recordPlanError := func(err error) {
				if err != nil {
					plan.compensationErrors = append(plan.compensationErrors, err)
					rollbackErrors = append(rollbackErrors, err)
				}
			}
			if plan.worktreeOwned {
				if record, recordError := gitcmd.WorktreeAt(plan.attachment.MainCheckout, plan.ownedWorktreePath); recordError == nil {
					if record.BranchRef != plan.branchRef {
						recordPlanError(fmt.Errorf("refuse to remove changed Task Worktree record for repository %q: expected Task Branch Name ref %q, found %q", plan.attachment.Alias, plan.branchRef, record.BranchRef))
					} else if currentInfo, pathError := os.Lstat(plan.ownedWorktreePath); pathError != nil {
						recordPlanError(fmt.Errorf("inspect owned Task Worktree path for repository %q before removal: %w", plan.attachment.Alias, pathError))
					} else if plan.ownedWorktreeInfo == nil || !os.SameFile(plan.ownedWorktreeInfo, currentInfo) {
						recordPlanError(fmt.Errorf("refuse to remove changed Task Worktree path %q", plan.ownedWorktreePath))
					} else if removeError := gitcmd.RemoveWorktree(plan.attachment.MainCheckout, plan.ownedWorktreePath); removeError != nil {
						recordPlanError(fmt.Errorf("remove Task Worktree for repository %q: %w", plan.attachment.Alias, removeError))
					}
				} else if errors.Is(recordError, gitcmd.ErrWorktreeRecordNotFound) {
					if currentInfo, pathError := os.Lstat(plan.ownedWorktreePath); pathError == nil {
						if plan.ownedWorktreeInfo == nil || !os.SameFile(plan.ownedWorktreeInfo, currentInfo) {
							recordPlanError(fmt.Errorf("unregistered Task Worktree path changed at %q; refuse automatic removal", plan.ownedWorktreePath))
						} else if removeError := os.Remove(plan.ownedWorktreePath); removeError != nil {
							recordPlanError(fmt.Errorf("remove empty unregistered Task Worktree path %q: %w", plan.ownedWorktreePath, removeError))
						}
					} else if !errors.Is(pathError, os.ErrNotExist) {
						recordPlanError(fmt.Errorf("inspect failed Task Worktree path for repository %q: %w", plan.attachment.Alias, pathError))
					}
				} else {
					recordPlanError(fmt.Errorf("inspect Task Worktree record during rollback for repository %q: %w", plan.attachment.Alias, recordError))
				}
			} else if plan.worktreeAttempted {
				if record, recordError := gitcmd.WorktreeAt(plan.attachment.MainCheckout, plan.attachment.WorktreePath); recordError == nil {
					recordPlanError(fmt.Errorf("refuse to remove unowned Git worktree record at %q on %q", record.Path, record.BranchRef))
				} else if !errors.Is(recordError, gitcmd.ErrWorktreeRecordNotFound) {
					recordPlanError(fmt.Errorf("inspect possible Task Worktree residual for repository %q: %w", plan.attachment.Alias, recordError))
				}
				if _, pathError := os.Lstat(plan.attachment.WorktreePath); pathError == nil {
					recordPlanError(fmt.Errorf("unowned Task Worktree path remains at %q; refuse automatic removal", plan.attachment.WorktreePath))
				} else if !errors.Is(pathError, os.ErrNotExist) {
					recordPlanError(fmt.Errorf("inspect possible Task Worktree path residual for repository %q: %w", plan.attachment.Alias, pathError))
				}
				if exists, branchError := gitcmd.RefExists(plan.attachment.MainCheckout, plan.branchRef); branchError != nil {
					recordPlanError(fmt.Errorf("inspect possible Task Branch Name residual for repository %q: %w", plan.attachment.Alias, branchError))
				} else if exists && !plan.branchExisted && !plan.branchOwned {
					recordPlanError(fmt.Errorf("unowned Task Branch Name %q remains in repository %q; refuse automatic deletion", metadata.TaskBranchName, plan.attachment.Alias))
				}
			}
			if plan.branchOwned {
				if exists, branchError := gitcmd.RefExists(plan.attachment.MainCheckout, plan.branchRef); branchError != nil {
					recordPlanError(fmt.Errorf("inspect Task Branch Name during rollback for repository %q: %w", plan.attachment.Alias, branchError))
				} else if exists {
					if owner, ownerError := gitcmd.WorktreeForBranch(plan.attachment.MainCheckout, plan.branchRef); ownerError == nil {
						recordPlanError(fmt.Errorf("refuse to delete Task Branch Name for repository %q because it is assigned to Git worktree %q", plan.attachment.Alias, owner.Path))
					} else if !errors.Is(ownerError, gitcmd.ErrWorktreeRecordNotFound) {
						recordPlanError(fmt.Errorf("inspect Task Branch Name ownership during rollback for repository %q: %w", plan.attachment.Alias, ownerError))
					} else if deleteError := gitcmd.DeleteBranch(plan.attachment.MainCheckout, metadata.TaskBranchName); deleteError != nil {
						recordPlanError(fmt.Errorf("delete Task Branch Name for repository %q: %w", plan.attachment.Alias, deleteError))
					}
				}
			}
			if plan.worktreesRootInfo != nil {
				worktreesRoot := filepath.Dir(plan.attachment.WorktreePath)
				if currentInfo, inspectError := os.Lstat(worktreesRoot); inspectError == nil {
					if !os.SameFile(plan.worktreesRootInfo, currentInfo) {
						recordPlanError(fmt.Errorf("refuse to remove changed worktrees directory for repository %q", plan.attachment.Alias))
					} else if removeError := os.Remove(worktreesRoot); removeError != nil && !errors.Is(removeError, os.ErrNotExist) {
						recordPlanError(fmt.Errorf("remove empty worktrees directory for repository %q: %w", plan.attachment.Alias, removeError))
					}
				} else if !errors.Is(inspectError, os.ErrNotExist) {
					recordPlanError(fmt.Errorf("inspect worktrees directory during rollback for repository %q: %w", plan.attachment.Alias, inspectError))
				}
			}
			recordPlanError(plan.excludeChange.Abort())
			recordPlanError(recordCompensationForTest(plan.attachment.Alias))
		}
		if joined := errors.Join(rollbackErrors...); joined != nil {
			incompleteAttachments, residuals := observeResidualAttachments(metadata.Name, plans, paths, cause, joined)
			if persistError := persistIncompleteAttachments(metadataPath, incompleteAttachments, cause, joined, residuals); persistError != nil {
				return fmt.Errorf("%v; roll back Repository Attachments: %v; persist incomplete state: %w", cause, joined, persistError)
			}
			return fmt.Errorf("%v; roll back Repository Attachments: %v; Task %q is incomplete with residual state; run status and follow recovery guidance", cause, joined, metadata.Name)
		}
		return cause
	}

	for index := range plans {
		if err := plans[index].excludeChange.Commit(); err != nil {
			return nil, rollback(fmt.Errorf("prepare Task Worktree for repository %q: %w", plans[index].attachment.Alias, err))
		}
		if err := afterExcludeForTest(plans[index].attachment.Alias); err != nil {
			return nil, rollback(err)
		}
	}
	for index := range plans {
		plan := &plans[index]
		if rootError := plan.createWorktreesRoot(); rootError != nil {
			return nil, rollback(rootError)
		}
		if !plan.branchExisted {
			if err := gitcmd.CreateBranch(plan.attachment.MainCheckout, metadata.TaskBranchName, plan.attachment.BaseRef); err != nil {
				return nil, rollback(fmt.Errorf("create Task Branch Name for repository %q: %w", plan.attachment.Alias, err))
			}
			plan.branchOwned = true
		}
		if stageError := plan.createOwnedWorktreeStage(); stageError != nil {
			return nil, rollback(stageError)
		}
		plan.worktreeAttempted = true
		err = gitcmd.AttachWorktree(plan.attachment.MainCheckout, plan.ownedWorktreePath, metadata.TaskBranchName)
		if err != nil {
			return nil, rollback(fmt.Errorf("create Task Worktree for repository %q: %w", plan.attachment.Alias, err))
		}
		beforeWorktreeMoveForTest(plan.attachment.Alias)
		if moveError := gitcmd.MoveWorktree(plan.attachment.MainCheckout, plan.ownedWorktreePath, plan.attachment.WorktreePath); moveError != nil {
			return nil, rollback(fmt.Errorf("move Task Worktree into place for repository %q: %w", plan.attachment.Alias, moveError))
		}
		owner, ownerError := gitcmd.WorktreeForBranch(plan.attachment.MainCheckout, plan.branchRef)
		if ownerError != nil {
			return nil, rollback(fmt.Errorf("locate moved Task Worktree for repository %q: %w", plan.attachment.Alias, ownerError))
		}
		plan.ownedWorktreePath = owner.Path
		if movedInfo, movedError := os.Lstat(plan.ownedWorktreePath); movedError != nil {
			return nil, rollback(fmt.Errorf("inspect moved Task Worktree for repository %q: %w", plan.attachment.Alias, movedError))
		} else if !os.SameFile(plan.ownedWorktreeInfo, movedInfo) {
			return nil, rollback(fmt.Errorf("moved Task Worktree for repository %q changed identity", plan.attachment.Alias))
		}
		if filepath.Clean(plan.ownedWorktreePath) != filepath.Clean(plan.attachment.WorktreePath) {
			return nil, rollback(fmt.Errorf("move Task Worktree for repository %q selected unexpected destination %q", plan.attachment.Alias, plan.ownedWorktreePath))
		}
		canonicalWorktree, resolveError := filepath.EvalSymlinks(plan.attachment.WorktreePath)
		if resolveError != nil {
			return nil, rollback(fmt.Errorf("resolve created Task Worktree for repository %q: %w", plan.attachment.Alias, resolveError))
		}
		if canonicalWorktree != plan.attachment.WorktreePath {
			return nil, rollback(fmt.Errorf("created Task Worktree for repository %q resolved outside its expected path: %q", plan.attachment.Alias, canonicalWorktree))
		}
		plan.attachment.WorktreePath = canonicalWorktree
		if err := afterWorktreeForTest(plan.attachment.Alias); err != nil {
			return nil, rollback(err)
		}
	}
	if projection != nil {
		if err := projection.Commit(); err != nil {
			return nil, rollback(err)
		}
		if err := afterProjectionForTest(); err != nil {
			return nil, rollback(err)
		}
	}
	for _, plan := range plans {
		metadata.Attachments = append(metadata.Attachments, plan.attachment)
	}
	if projection != nil {
		metadata.ContextFiles = projection.RefreshOwnedContextFiles(metadata.ContextFiles)
	}
	updatedMetadata, err := yaml.Marshal(metadata)
	if err != nil {
		return nil, rollback(fmt.Errorf("encode Task metadata: %w", err))
	}
	outcome, err := fileutil.WriteAtomicIfUnchanged(metadataPath, originalMetadata, updatedMetadata, 0o600)
	if err != nil {
		if outcome.Published {
			return nil, fmt.Errorf("Repository Attachment metadata was published but could not be durably synced: %w; inspect the Task before retrying", err)
		}
		return nil, rollback(fmt.Errorf("update Task metadata: %w", err))
	}
	results := make([]AddResult, 0, len(requests))
	for _, request := range requests {
		if request.existingIndex >= 0 {
			results = append(results, AddResult{TaskName: metadata.Name, Attachment: metadata.Attachments[request.existingIndex], AlreadyAttached: true})
		} else {
			results = append(results, AddResult{TaskName: metadata.Name, Attachment: plans[request.planIndex].attachment})
		}
	}
	return results, nil
}

func addRepositoryLockTarget(targets map[string]*repositoryLockTarget, path, alias string) {
	if existing := targets[path]; existing != nil {
		existing.aliases = append(existing.aliases, alias)
		return
	}
	targets[path] = &repositoryLockTarget{path: path, aliases: []string{alias}}
}

func closeRepositoryLocks(targets []*repositoryLockTarget) {
	for index := len(targets) - 1; index >= 0; index-- {
		if targets[index].lock != nil {
			_ = targets[index].lock.Close()
			targets[index].lock = nil
		}
	}
}

func (plan *addPlan) createWorktreesRoot() error {
	if !plan.worktreesRootWasAbsent {
		return nil
	}
	worktreesRoot := filepath.Dir(plan.attachment.WorktreePath)
	if err := os.Mkdir(worktreesRoot, 0o700); err != nil {
		return fmt.Errorf("create worktrees directory for repository %q: %w", plan.attachment.Alias, err)
	}
	info, err := os.Lstat(worktreesRoot)
	if err != nil {
		return fmt.Errorf("inspect created worktrees directory for repository %q: %w", plan.attachment.Alias, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("created worktrees path %q is not a real directory", worktreesRoot)
	}
	plan.worktreesRootInfo = info
	return nil
}

func (plan *addPlan) createOwnedWorktreeStage() error {
	worktreesRoot := filepath.Dir(plan.attachment.WorktreePath)
	stagePath, err := os.MkdirTemp(worktreesRoot, ".devtask-worktree-")
	if err != nil {
		return fmt.Errorf("create Task Worktree staging directory for repository %q: %w", plan.attachment.Alias, err)
	}
	info, err := os.Lstat(stagePath)
	if err != nil {
		_ = os.Remove(stagePath)
		return fmt.Errorf("inspect Task Worktree staging directory for repository %q: %w", plan.attachment.Alias, err)
	}
	plan.worktreeOwned = true
	plan.ownedWorktreePath = stagePath
	plan.ownedWorktreeInfo = info
	return nil
}

func observeResidualAttachments(taskName string, plans []addPlan, paths config.Paths, cause, rollbackError error) ([]RepositoryAttachment, []string) {
	operationResiduals := []string{"rollback error: " + rollbackError.Error()}
	incomplete := make([]RepositoryAttachment, 0, len(plans))
	for _, plan := range plans {
		attachment := plan.attachment
		attachmentResiduals := make([]string, 0, 6)
		for _, compensationError := range plan.compensationErrors {
			attachmentResiduals = append(attachmentResiduals, "compensation error: "+compensationError.Error())
		}
		if _, err := os.Lstat(attachment.WorktreePath); err == nil {
			attachmentResiduals = append(attachmentResiduals, "Task Worktree path remains: "+attachment.WorktreePath)
		}
		if record, err := gitcmd.WorktreeAt(attachment.MainCheckout, attachment.WorktreePath); err == nil {
			attachmentResiduals = append(attachmentResiduals, "Git worktree record remains: "+record.Path)
		}
		if plan.ownedWorktreePath != "" && plan.ownedWorktreePath != attachment.WorktreePath {
			if _, err := os.Lstat(plan.ownedWorktreePath); err == nil {
				attachmentResiduals = append(attachmentResiduals, "Task Worktree staging path remains: "+plan.ownedWorktreePath)
			}
			if record, err := gitcmd.WorktreeAt(attachment.MainCheckout, plan.ownedWorktreePath); err == nil {
				attachmentResiduals = append(attachmentResiduals, "Git worktree staging record remains: "+record.Path)
			}
		}
		if !plan.branchExisted {
			if exists, err := gitcmd.RefExists(attachment.MainCheckout, "refs/heads/"+attachment.TaskBranchName); err == nil && exists {
				attachmentResiduals = append(attachmentResiduals, "Task Branch Name remains: "+attachment.TaskBranchName)
			}
		}
		linkPath := filepath.Join(paths.Workspaces, taskName, attachment.Alias)
		if _, err := os.Lstat(linkPath); err == nil {
			attachmentResiduals = append(attachmentResiduals, "Task Workspace entry remains: "+linkPath)
		}
		if plan.worktreesRootWasAbsent {
			worktreesRoot := filepath.Dir(attachment.WorktreePath)
			if _, err := os.Lstat(worktreesRoot); err == nil {
				attachmentResiduals = append(attachmentResiduals, "worktrees directory remains: "+worktreesRoot)
			}
		}
		if len(attachmentResiduals) == 0 {
			continue
		}
		attachment.State = StateIncomplete
		attachment.LastError = cause.Error()
		attachment.ResidualObjects = attachmentResiduals
		incomplete = append(incomplete, attachment)
		operationResiduals = append(operationResiduals, attachmentResiduals...)
	}
	return incomplete, operationResiduals
}

func persistIncompleteAttachments(metadataPath string, attachments []RepositoryAttachment, cause, rollbackError error, residuals []string) error {
	current, err := os.ReadFile(metadataPath)
	if err != nil {
		return err
	}
	metadata, err := load(metadataPath)
	if err != nil {
		return err
	}
	metadata.State = StateIncomplete
	metadata.Attachments = append(metadata.Attachments, attachments...)
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
