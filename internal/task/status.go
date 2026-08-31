package task

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Leonz3n/devtask/internal/config"
	gitcmd "github.com/Leonz3n/devtask/internal/git"
)

type StatusReport struct {
	Name                string               `json:"name"`
	TaskBranchName      string               `json:"task_branch_name"`
	CreatedAt           time.Time            `json:"created_at"`
	State               State                `json:"state"`
	WorkspacePath       string               `json:"workspace_path"`
	Missing             bool                 `json:"missing"`
	Unknown             bool                 `json:"unknown"`
	Incomplete          bool                 `json:"incomplete"`
	Inspection          *InspectionIssue     `json:"inspection"`
	IncompleteOperation *IncompleteOperation `json:"incomplete_operation"`
	Attachments         []AttachmentStatus   `json:"attachments"`
}

type AttachmentStatus struct {
	Alias           string           `json:"alias"`
	MainCheckout    string           `json:"main_checkout"`
	WorktreePath    string           `json:"worktree_path"`
	TaskBranchName  string           `json:"task_branch_name"`
	Clean           bool             `json:"clean"`
	Modified        bool             `json:"modified"`
	Staged          bool             `json:"staged"`
	Untracked       bool             `json:"untracked"`
	Conflicted      bool             `json:"conflicted"`
	Missing         bool             `json:"missing"`
	Unknown         bool             `json:"unknown"`
	Incomplete      bool             `json:"incomplete"`
	Inspection      *InspectionIssue `json:"inspection"`
	LastError       string           `json:"last_error"`
	ResidualObjects []string         `json:"residual_objects"`
}

type InspectionIssue struct {
	Operation string   `json:"operation"`
	Message   string   `json:"message"`
	Recovery  []string `json:"recovery"`
}

func Status(paths config.Paths, requested string) (StatusReport, error) {
	if err := ValidateName(requested); err != nil {
		return StatusReport{}, err
	}
	_, _, metadata, err := loadForUpdate(paths, requested)
	if err != nil {
		return StatusReport{}, err
	}
	report := StatusReport{
		Name:                metadata.Name,
		TaskBranchName:      metadata.TaskBranchName,
		CreatedAt:           metadata.CreatedAt.UTC(),
		State:               metadata.State,
		WorkspacePath:       filepath.Join(paths.Workspaces, metadata.Name),
		Incomplete:          metadata.State == StateIncomplete,
		IncompleteOperation: metadata.Incomplete,
		Attachments:         make([]AttachmentStatus, 0, len(metadata.Attachments)),
	}
	inspectTaskWorkspace(metadata, &report)
	for _, attachment := range metadata.Attachments {
		attachmentStatus := AttachmentStatus{
			Alias:           attachment.Alias,
			MainCheckout:    attachment.MainCheckout,
			WorktreePath:    attachment.WorktreePath,
			TaskBranchName:  attachment.TaskBranchName,
			Incomplete:      attachment.State == StateIncomplete,
			LastError:       attachment.LastError,
			ResidualObjects: append([]string{}, attachment.ResidualObjects...),
		}
		inspectAttachment(paths, metadata, attachment, &attachmentStatus)
		attachmentStatus.Clean = !attachmentStatus.Modified && !attachmentStatus.Staged && !attachmentStatus.Untracked && !attachmentStatus.Conflicted && !attachmentStatus.Missing && !attachmentStatus.Unknown && !attachmentStatus.Incomplete
		report.Attachments = append(report.Attachments, attachmentStatus)
	}
	return report, nil
}

func inspectTaskWorkspace(metadata Metadata, report *StatusReport) {
	taskRecovery := []string{fmt.Sprintf("restore the Task Workspace at %q, then run 'devtask status %s' again", report.WorkspacePath, metadata.Name)}
	if issue, missing := inspectDirectory(report.WorkspacePath, "inspect Task Workspace path", taskRecovery); issue != nil {
		report.Inspection = issue
		report.Missing = missing
		report.Unknown = !missing
		return
	}
	for _, contextFile := range metadata.ContextFiles {
		path := filepath.Join(report.WorkspacePath, contextFile.Path)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			report.Inspection = missingInspection("inspect Task Context File", fmt.Sprintf("recorded Task Context File %q is missing", path), fmt.Sprintf("restore %q, then run 'devtask status %s' again", path, metadata.Name))
			report.Missing = true
			return
		}
		if err != nil {
			report.Inspection = unknownInspection("inspect Task Context File", err)
			report.Unknown = true
			return
		}
		if !info.Mode().IsRegular() {
			report.Inspection = missingInspection("inspect Task Context File", fmt.Sprintf("recorded Task Context File %q is no longer a regular file", path), fmt.Sprintf("restore %q as a regular file, then run 'devtask status %s' again", path, metadata.Name))
			report.Missing = true
			return
		}
	}
}

func inspectAttachment(paths config.Paths, metadata Metadata, attachment RepositoryAttachment, status *AttachmentStatus) {
	recovery := []string{
		fmt.Sprintf("restore Repository Attachment %q for Task %q, then run 'devtask status %s' again", attachment.Alias, metadata.Name, metadata.Name),
		fmt.Sprintf("if both its Task Worktree path and Git record are absent, run 'devtask remove-repo %s %s --forget'", metadata.Name, attachment.Alias),
	}
	if issue, missing := inspectDirectory(attachment.MainCheckout, "inspect Main Checkout path", recovery); issue != nil {
		setInspection(status, issue, missing)
		return
	}
	if issue, missing := inspectDirectory(attachment.WorktreePath, "inspect Task Worktree path", recovery); issue != nil {
		setInspection(status, issue, missing)
		return
	}
	canonicalWorktree, err := filepath.EvalSymlinks(attachment.WorktreePath)
	if err != nil {
		setInspection(status, unknownInspection("resolve Task Worktree path", err), false)
		return
	}
	if canonicalWorktree != attachment.WorktreePath {
		setInspection(status, missingInspection("resolve Task Worktree path", fmt.Sprintf("recorded path %q resolves to %q", attachment.WorktreePath, canonicalWorktree), recovery...), true)
		return
	}
	record, err := gitcmd.WorktreeAt(attachment.MainCheckout, attachment.WorktreePath)
	if errors.Is(err, gitcmd.ErrWorktreeRecordNotFound) {
		setInspection(status, missingInspection("inspect Git worktree record", fmt.Sprintf("no Git worktree record exists for %q", attachment.WorktreePath), recovery...), true)
		return
	}
	if err != nil {
		setInspection(status, unknownInspection("inspect Git worktree record", err), false)
		return
	}
	expectedBranchRef := "refs/heads/" + attachment.TaskBranchName
	if record.Prunable || record.BranchRef != expectedBranchRef {
		message := fmt.Sprintf("record for %q has Task Branch Name %q; expected %q", attachment.WorktreePath, record.BranchRef, expectedBranchRef)
		if record.Prunable {
			message = fmt.Sprintf("record for %q is prunable", attachment.WorktreePath)
		}
		setInspection(status, missingInspection("inspect Git worktree record", message, recovery...), true)
		return
	}
	linkPath := filepath.Join(paths.Workspaces, metadata.Name, attachment.Alias)
	linkInfo, err := os.Lstat(linkPath)
	if errors.Is(err, os.ErrNotExist) {
		setInspection(status, missingInspection("inspect Task Workspace link", fmt.Sprintf("Task Workspace link %q is missing", linkPath), recovery...), true)
		return
	}
	if err != nil {
		setInspection(status, unknownInspection("inspect Task Workspace link", err), false)
		return
	}
	if linkInfo.Mode()&os.ModeSymlink == 0 {
		setInspection(status, missingInspection("inspect Task Workspace link", fmt.Sprintf("Task Workspace entry %q is not the recorded symlink", linkPath), recovery...), true)
		return
	}
	resolvedLink, err := filepath.EvalSymlinks(linkPath)
	if err != nil {
		setInspection(status, unknownInspection("resolve Task Workspace link", err), false)
		return
	}
	if resolvedLink != attachment.WorktreePath {
		setInspection(status, missingInspection("resolve Task Workspace link", fmt.Sprintf("Task Workspace link %q resolves to %q; expected %q", linkPath, resolvedLink, attachment.WorktreePath), recovery...), true)
		return
	}
	porcelain, err := gitcmd.Run(attachment.WorktreePath, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		setInspection(status, unknownInspection("inspect Git status", err), false)
		return
	}
	gitStatus, err := gitcmd.ParseStatusPorcelainV1Z(porcelain)
	if err != nil {
		setInspection(status, unknownInspection("parse Git status", err), false)
		return
	}
	status.Modified = gitStatus.Modified
	status.Staged = gitStatus.Staged
	status.Untracked = gitStatus.Untracked
	status.Conflicted = gitStatus.Conflicted
}

func inspectDirectory(path, operation string, recovery []string) (*InspectionIssue, bool) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return missingInspection(operation, fmt.Sprintf("recorded directory %q is missing", path), recovery...), true
	}
	if err != nil {
		return unknownInspection(operation, err), false
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return missingInspection(operation, fmt.Sprintf("recorded directory %q is no longer a real directory", path), recovery...), true
	}
	return nil, false
}

func missingInspection(operation, message string, recovery ...string) *InspectionIssue {
	return &InspectionIssue{
		Operation: operation,
		Message:   message,
		Recovery:  recovery,
	}
}

func unknownInspection(operation string, err error) *InspectionIssue {
	return &InspectionIssue{
		Operation: operation,
		Message:   err.Error(),
		Recovery:  []string{"resolve the reported filesystem or Git error, then run status again"},
	}
}

func setInspection(status *AttachmentStatus, issue *InspectionIssue, missing bool) {
	status.Inspection = issue
	status.Missing = missing
	status.Unknown = !missing
}
