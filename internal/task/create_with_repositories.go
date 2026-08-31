package task

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Leonz3n/devtask/internal/config"
	"github.com/Leonz3n/devtask/internal/fileutil"
	"github.com/Leonz3n/devtask/internal/lock"
	"gopkg.in/yaml.v3"
)

type CreateWithRepositoriesResult struct {
	Metadata    Metadata
	Attachments []AddResult
}

func CreateWithRepositories(paths config.Paths, configuration config.Config, name string, branchOverride *string, repositoryAliases []string, baseOverride *string, fetchOverride *bool) (CreateWithRepositoriesResult, error) {
	if err := ValidateName(name); err != nil {
		return CreateWithRepositoriesResult{}, err
	}
	taskLock, err := lock.Acquire(lockPath(paths, name))
	if err != nil {
		if errors.Is(err, lock.ErrBusy) {
			return CreateWithRepositoriesResult{}, fmt.Errorf("Task %q is busy: another devtask process holds its lock", name)
		}
		return CreateWithRepositoriesResult{}, err
	}
	defer taskLock.Close()

	creation, err := createLocked(paths, configuration, name, branchOverride)
	if err != nil {
		return CreateWithRepositoriesResult{}, err
	}
	if len(repositoryAliases) == 0 {
		return CreateWithRepositoriesResult{Metadata: creation.metadata, Attachments: []AddResult{}}, nil
	}
	attachments, err := addRepositoriesLocked(paths, configuration, name, repositoryAliases, baseOverride, fetchOverride)
	if err == nil {
		metadata, loadError := load(creation.metadataPath)
		if loadError != nil {
			return CreateWithRepositoriesResult{}, fmt.Errorf("load completed grouped Task %q: %w", name, loadError)
		}
		return CreateWithRepositoriesResult{Metadata: metadata, Attachments: attachments}, nil
	}
	return CreateWithRepositoriesResult{}, compensateGroupedCreation(creation, err)
}

func compensateGroupedCreation(creation *taskCreation, cause error) error {
	current, loadError := load(creation.metadataPath)
	if loadError == nil && current.State == StateIncomplete {
		if persistError := persistGroupedCreationIncomplete(creation.metadataPath, creation.workspace.Path, cause, nil); persistError != nil {
			return fmt.Errorf("%v; identify grouped Task failure: %w", cause, persistError)
		}
		return cause
	}

	workspaceError := creation.workspace.Abort()
	if workspaceError != nil {
		cleanupError := fmt.Errorf("remove new Task Workspace: %w", workspaceError)
		if persistError := persistGroupedCreationIncomplete(creation.metadataPath, creation.workspace.Path, cause, cleanupError); persistError != nil {
			return fmt.Errorf("%v; roll back grouped Task creation: %v; persist incomplete state: %w", cause, cleanupError, persistError)
		}
		return fmt.Errorf("%v; roll back grouped Task creation: %v; Task %q is incomplete with residual state; run status and follow recovery guidance", cause, cleanupError, creation.metadata.Name)
	}
	if metadataError := removePublishedMetadata(creation.metadataPath, creation.metadataIdentity, creation.metadataContents); metadataError != nil {
		cleanupError := fmt.Errorf("remove new Task metadata: %w", metadataError)
		if _, statError := os.Lstat(creation.metadataPath); statError == nil {
			if persistError := persistGroupedCreationIncomplete(creation.metadataPath, creation.workspace.Path, cause, cleanupError); persistError != nil {
				return fmt.Errorf("%v; roll back grouped Task creation: %v; persist incomplete state: %w", cause, cleanupError, persistError)
			}
		}
		return fmt.Errorf("%v; roll back grouped Task creation: %v; inspect %q before retrying", cause, cleanupError, creation.metadataPath)
	}
	return cause
}

func persistGroupedCreationIncomplete(metadataPath, workspacePath string, cause, cleanupError error) error {
	original, err := os.ReadFile(metadataPath)
	if err != nil {
		return err
	}
	metadata, err := load(metadataPath)
	if err != nil {
		return err
	}
	residuals := make([]string, 0, 2)
	if metadata.Incomplete != nil {
		residuals = append(residuals, metadata.Incomplete.ResidualObjects...)
	}
	if _, err := os.Lstat(workspacePath); err == nil {
		residuals = append(residuals, "Task Workspace remains: "+workspacePath)
	}
	if _, err := os.Lstat(metadataPath); err == nil {
		residuals = append(residuals, "Task metadata remains: "+metadataPath)
	}
	lastError := cause.Error()
	if cleanupError != nil {
		lastError += "; rollback: " + cleanupError.Error()
	}
	metadata.State = StateIncomplete
	for index := range metadata.Attachments {
		attachment := &metadata.Attachments[index]
		attachment.State = StateIncomplete
		attachment.LastError = lastError
		if _, err := os.Lstat(attachment.WorktreePath); err == nil {
			attachment.ResidualObjects = appendUnique(attachment.ResidualObjects, "Task Worktree path remains: "+attachment.WorktreePath)
		}
		workspaceEntry := filepath.Join(workspacePath, attachment.Alias)
		if _, err := os.Lstat(workspaceEntry); err == nil {
			attachment.ResidualObjects = appendUnique(attachment.ResidualObjects, "Task Workspace entry remains: "+workspaceEntry)
		}
		for _, residual := range attachment.ResidualObjects {
			residuals = appendUnique(residuals, residual)
		}
	}
	metadata.Incomplete = &IncompleteOperation{
		Operation:       "create_group",
		LastError:       lastError,
		ResidualObjects: residuals,
		Recovery: []string{
			"inspect each residual object before changing it",
			"restore or remove residual grouped Task objects, then retry recovery",
		},
	}
	updated, err := yaml.Marshal(metadata)
	if err != nil {
		return err
	}
	outcome, err := fileutil.WriteAtomicIfUnchanged(metadataPath, original, updated, 0o600)
	if err != nil {
		if outcome.Published {
			return fmt.Errorf("incomplete grouped Task state was published but not durably synced: %w", err)
		}
		return err
	}
	return nil
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
