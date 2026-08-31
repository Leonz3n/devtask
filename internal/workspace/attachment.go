package workspace

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Leonz3n/devtask/internal/fileutil"
)

type ManagedLink struct {
	Path   string `yaml:"path"`
	Target string `yaml:"target"`
}

type Attachment struct {
	Alias        string
	WorktreePath string
}

type Projection struct {
	workspacePath   string
	agentsPath      string
	linkPath        string
	linkTarget      string
	original        []byte
	updated         []byte
	agentsInfo      os.FileInfo
	linkCreated     bool
	agentsPublished bool
}

func PrepareProjection(workspacePath, taskName, taskBranchName, alias, worktreePath string, attachments []Attachment) (*Projection, error) {
	workspaceInfo, err := os.Lstat(workspacePath)
	if err != nil {
		return nil, fmt.Errorf("inspect Task Workspace %q: %w", workspacePath, err)
	}
	if !workspaceInfo.IsDir() || workspaceInfo.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w at %q: expected a directory", ErrCollision, workspacePath)
	}
	canonicalWorkspace, err := filepath.EvalSymlinks(workspacePath)
	if err != nil {
		return nil, fmt.Errorf("resolve Task Workspace %q: %w", workspacePath, err)
	}
	linkPath := filepath.Join(workspacePath, alias)
	if _, err := os.Lstat(linkPath); err == nil {
		return nil, fmt.Errorf("%w at %q: Repository Alias path already exists", ErrCollision, linkPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect Task Workspace link %q: %w", linkPath, err)
	}
	agentsPath := filepath.Join(workspacePath, "AGENTS.md")
	agentsInfo, err := os.Lstat(agentsPath)
	if err != nil {
		return nil, fmt.Errorf("inspect Task Context File %q: %w", agentsPath, err)
	}
	if !agentsInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("%w at %q: AGENTS.md is not a regular file", ErrCollision, agentsPath)
	}
	original, err := os.ReadFile(agentsPath)
	if err != nil {
		return nil, fmt.Errorf("read Task Context File %q: %w", agentsPath, err)
	}
	updated, err := replaceGeneratedSection(original, generatedSection(taskName, taskBranchName, attachments))
	if err != nil {
		return nil, fmt.Errorf("%w at %q: %v", ErrCollision, agentsPath, err)
	}
	linkTarget, err := filepath.Rel(canonicalWorkspace, worktreePath)
	if err != nil {
		return nil, fmt.Errorf("calculate Task Workspace link for %q: %w", alias, err)
	}
	return &Projection{
		workspacePath: workspacePath,
		agentsPath:    agentsPath,
		linkPath:      linkPath,
		linkTarget:    linkTarget,
		original:      original,
		updated:       updated,
		agentsInfo:    agentsInfo,
	}, nil
}

func (projection *Projection) Commit() error {
	currentInfo, err := os.Lstat(projection.agentsPath)
	if err != nil {
		return fmt.Errorf("recheck AGENTS.md: %w", err)
	}
	current, err := os.ReadFile(projection.agentsPath)
	if err != nil {
		return fmt.Errorf("recheck AGENTS.md: %w", err)
	}
	if !os.SameFile(projection.agentsInfo, currentInfo) || !bytes.Equal(current, projection.original) {
		return fmt.Errorf("%w at %q: AGENTS.md changed during add", ErrCollision, projection.agentsPath)
	}
	if _, err := os.Lstat(projection.linkPath); err == nil {
		return fmt.Errorf("%w at %q: Repository Alias path appeared during add", ErrCollision, projection.linkPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("recheck Task Workspace link %q: %w", projection.linkPath, err)
	}
	if err := os.Symlink(projection.linkTarget, projection.linkPath); err != nil {
		return fmt.Errorf("create Task Workspace link %q: %w", projection.linkPath, err)
	}
	projection.linkCreated = true
	outcome, err := fileutil.WriteAtomicIfUnchanged(projection.agentsPath, projection.original, projection.updated, 0o600)
	if err != nil {
		projection.agentsPublished = outcome.Published
		if !outcome.Published {
			if removeError := os.Remove(projection.linkPath); removeError != nil {
				return errors.Join(fmt.Errorf("update AGENTS.md: %w", err), fmt.Errorf("remove Task Workspace link: %w", removeError))
			}
			projection.linkCreated = false
		}
		return fmt.Errorf("update AGENTS.md: %w", err)
	}
	projection.agentsPublished = true
	return fileutil.SyncDirectory(projection.workspacePath)
}

func (projection *Projection) RefreshOwnedContextFiles(files []ContextFile) []ContextFile {
	refreshed := append([]ContextFile(nil), files...)
	originalDigest := sha256.Sum256(projection.original)
	updatedDigest := sha256.Sum256(projection.updated)
	for index := range refreshed {
		if refreshed[index].Path == "AGENTS.md" && refreshed[index].SHA256 == hex.EncodeToString(originalDigest[:]) {
			refreshed[index].SHA256 = hex.EncodeToString(updatedDigest[:])
		}
	}
	return refreshed
}

func (projection *Projection) Abort() error {
	if !projection.linkCreated && !projection.agentsPublished {
		return nil
	}
	var failures []error
	if projection.agentsPublished {
		outcome, err := fileutil.WriteAtomicIfUnchanged(projection.agentsPath, projection.updated, projection.original, 0o600)
		if err != nil {
			failures = append(failures, fmt.Errorf("restore AGENTS.md: %w", err))
		} else if !outcome.Published {
			failures = append(failures, fmt.Errorf("restore AGENTS.md %q was not published", projection.agentsPath))
		} else {
			projection.agentsPublished = false
		}
	}
	if projection.linkCreated {
		target, err := os.Readlink(projection.linkPath)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				failures = append(failures, fmt.Errorf("inspect Task Workspace link during rollback: %w", err))
			} else {
				projection.linkCreated = false
			}
		} else if target != projection.linkTarget {
			failures = append(failures, fmt.Errorf("refuse to remove changed Task Workspace link %q", projection.linkPath))
		} else if err := os.Remove(projection.linkPath); err != nil {
			failures = append(failures, fmt.Errorf("remove Task Workspace link: %w", err))
		} else {
			projection.linkCreated = false
		}
	}
	return errors.Join(failures...)
}

func replaceGeneratedSection(document, generated []byte) ([]byte, error) {
	start := []byte(GeneratedStart)
	end := []byte(GeneratedEnd)
	if bytes.Count(document, start) != 1 || bytes.Count(document, end) != 1 {
		return nil, errors.New("AGENTS.md must contain exactly one generated marker pair")
	}
	startIndex := bytes.Index(document, start)
	endIndex := bytes.Index(document, end)
	if endIndex < startIndex {
		return nil, errors.New("AGENTS.md generated markers are out of order")
	}
	endIndex += len(end)
	result := make([]byte, 0, len(document)+len(generated))
	result = append(result, document[:startIndex]...)
	result = append(result, generated...)
	result = append(result, document[endIndex:]...)
	return result, nil
}

func generatedSection(taskName, taskBranchName string, attachments []Attachment) []byte {
	var output strings.Builder
	fmt.Fprintf(&output, "%s\n## Task\n\n- Name: `%s`\n- Task Branch Name: `%s`\n\n## Repository Attachments\n\n", GeneratedStart, taskName, taskBranchName)
	if len(attachments) == 0 {
		output.WriteString("None.\n")
	} else {
		for _, attachment := range attachments {
			fmt.Fprintf(&output, "- `%s`: `%s`\n", attachment.Alias, attachment.WorktreePath)
		}
	}
	fmt.Fprintf(&output, "%s", GeneratedEnd)
	return []byte(output.String())
}
