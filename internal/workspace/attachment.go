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
	Source      string `yaml:"source"`
	Destination string `yaml:"destination"`
	Target      string `yaml:"target"`
}

type Attachment struct {
	Alias        string
	WorktreePath string
}

type Projection struct {
	workspacePath   string
	agentsPath      string
	links           []projectionLink
	original        []byte
	updated         []byte
	agentsInfo      os.FileInfo
	agentsPublished bool
}

type projectionLink struct {
	path    string
	target  string
	created bool
}

type RemovalProjection struct {
	workspacePath string
	agentsPath    string
	linkPath      string
	linkTarget    string
	linkInfo      os.FileInfo
	original      []byte
	updated       []byte
	agentsInfo    os.FileInfo
}

func PrepareRemovalProjection(workspacePath, taskName, taskBranchName string, removed Attachment, remaining []Attachment, allowMissingLink bool) (*RemovalProjection, error) {
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
	if removed.Alias == "" || removed.Alias == "." || removed.Alias == ".." || filepath.Base(removed.Alias) != removed.Alias {
		return nil, fmt.Errorf("%w: Repository Alias %q does not name a direct Task Workspace entry", ErrCollision, removed.Alias)
	}
	linkPath := filepath.Join(workspacePath, removed.Alias)
	canonicalLinkParent, err := filepath.EvalSymlinks(filepath.Dir(linkPath))
	if err != nil || canonicalLinkParent != canonicalWorkspace {
		return nil, fmt.Errorf("%w at %q: Repository Alias path escapes the Task Workspace", ErrCollision, linkPath)
	}
	linkTarget, err := filepath.Rel(canonicalWorkspace, removed.WorktreePath)
	if err != nil {
		return nil, fmt.Errorf("calculate Task Workspace link for %q: %w", removed.Alias, err)
	}
	linkInfo, err := os.Lstat(linkPath)
	if errors.Is(err, os.ErrNotExist) && allowMissingLink {
		linkInfo = nil
	} else if err != nil {
		return nil, fmt.Errorf("inspect Task Workspace link %q: %w", linkPath, err)
	} else if linkInfo.Mode()&os.ModeSymlink == 0 {
		return nil, fmt.Errorf("%w at %q: Repository Alias path is not the recorded symlink", ErrCollision, linkPath)
	} else if target, readError := os.Readlink(linkPath); readError != nil {
		return nil, fmt.Errorf("inspect Task Workspace link %q: %w", linkPath, readError)
	} else if target != linkTarget {
		return nil, fmt.Errorf("%w at %q: Repository Alias link target changed", ErrCollision, linkPath)
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
	updated, err := replaceGeneratedSection(original, generatedSection(taskName, taskBranchName, remaining))
	if err != nil {
		return nil, fmt.Errorf("%w at %q: %v", ErrCollision, agentsPath, err)
	}
	return &RemovalProjection{workspacePath: workspacePath, agentsPath: agentsPath, linkPath: linkPath, linkTarget: linkTarget, linkInfo: linkInfo, original: original, updated: updated, agentsInfo: agentsInfo}, nil
}

func (projection *RemovalProjection) Commit() error {
	currentAgentsInfo, err := os.Lstat(projection.agentsPath)
	if err != nil {
		return fmt.Errorf("recheck AGENTS.md: %w", err)
	}
	currentAgents, err := os.ReadFile(projection.agentsPath)
	if err != nil {
		return fmt.Errorf("recheck AGENTS.md: %w", err)
	}
	if !os.SameFile(projection.agentsInfo, currentAgentsInfo) || !bytes.Equal(currentAgents, projection.original) {
		return fmt.Errorf("%w at %q: AGENTS.md changed during removal", ErrCollision, projection.agentsPath)
	}
	if projection.linkInfo != nil {
		currentLinkInfo, err := os.Lstat(projection.linkPath)
		if err != nil {
			return fmt.Errorf("recheck Task Workspace link %q: %w", projection.linkPath, err)
		}
		target, err := os.Readlink(projection.linkPath)
		if err != nil || !os.SameFile(projection.linkInfo, currentLinkInfo) || target != projection.linkTarget {
			return fmt.Errorf("%w at %q: Repository Alias link changed during removal", ErrCollision, projection.linkPath)
		}
		if err := os.Remove(projection.linkPath); err != nil {
			return fmt.Errorf("remove Task Workspace link %q: %w", projection.linkPath, err)
		}
	}
	outcome, err := fileutil.WriteAtomicIfUnchanged(projection.agentsPath, projection.original, projection.updated, 0o600)
	if err != nil {
		if projection.linkInfo != nil && !outcome.Published {
			err = errors.Join(err, os.Symlink(projection.linkTarget, projection.linkPath))
		}
		return fmt.Errorf("update AGENTS.md during removal: %w", err)
	}
	return fileutil.SyncDirectory(projection.workspacePath)
}

func (projection *RemovalProjection) RefreshOwnedContextFiles(files []ContextFile) []ContextFile {
	return refreshOwnedContextFiles(files, projection.original, projection.updated)
}

func PrepareProjection(workspacePath, taskName, taskBranchName, alias, worktreePath string, attachments []Attachment) (*Projection, error) {
	return PrepareProjectionBatch(workspacePath, taskName, taskBranchName, []Attachment{{Alias: alias, WorktreePath: worktreePath}}, attachments)
}

func PrepareProjectionBatch(workspacePath, taskName, taskBranchName string, additions, attachments []Attachment) (*Projection, error) {
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
	links := make([]projectionLink, 0, len(additions))
	for _, addition := range additions {
		linkPath := filepath.Join(workspacePath, addition.Alias)
		if _, err := os.Lstat(linkPath); err == nil {
			return nil, fmt.Errorf("%w at %q: Repository Alias path already exists", ErrCollision, linkPath)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect Task Workspace link %q: %w", linkPath, err)
		}
		linkTarget, err := filepath.Rel(canonicalWorkspace, addition.WorktreePath)
		if err != nil {
			return nil, fmt.Errorf("calculate Task Workspace link for %q: %w", addition.Alias, err)
		}
		links = append(links, projectionLink{path: linkPath, target: linkTarget})
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
	return &Projection{
		workspacePath: workspacePath,
		agentsPath:    agentsPath,
		links:         links,
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
	for _, link := range projection.links {
		if _, err := os.Lstat(link.path); err == nil {
			return fmt.Errorf("%w at %q: Repository Alias path appeared during add", ErrCollision, link.path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("recheck Task Workspace link %q: %w", link.path, err)
		}
	}
	for index := range projection.links {
		link := &projection.links[index]
		if err := os.Symlink(link.target, link.path); err != nil {
			return fmt.Errorf("create Task Workspace link %q: %w", link.path, err)
		}
		link.created = true
	}
	outcome, err := fileutil.WriteAtomicIfUnchanged(projection.agentsPath, projection.original, projection.updated, 0o600)
	if err != nil {
		projection.agentsPublished = outcome.Published
		if !outcome.Published {
			return errors.Join(fmt.Errorf("update AGENTS.md: %w", err), projection.removeLinks())
		}
		return fmt.Errorf("update AGENTS.md: %w", err)
	}
	projection.agentsPublished = true
	return fileutil.SyncDirectory(projection.workspacePath)
}

func (projection *Projection) RefreshOwnedContextFiles(files []ContextFile) []ContextFile {
	return refreshOwnedContextFiles(files, projection.original, projection.updated)
}

func refreshOwnedContextFiles(files []ContextFile, original, updated []byte) []ContextFile {
	refreshed := append([]ContextFile(nil), files...)
	originalDigest := sha256.Sum256(original)
	updatedDigest := sha256.Sum256(updated)
	for index := range refreshed {
		if refreshed[index].Path == "AGENTS.md" && refreshed[index].SHA256 == hex.EncodeToString(originalDigest[:]) {
			refreshed[index].SHA256 = hex.EncodeToString(updatedDigest[:])
		}
	}
	return refreshed
}

func (projection *Projection) Abort() error {
	if !projection.hasCreatedLinks() && !projection.agentsPublished {
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
	failures = append(failures, projection.removeLinks())
	return errors.Join(failures...)
}

func (projection *Projection) hasCreatedLinks() bool {
	for _, link := range projection.links {
		if link.created {
			return true
		}
	}
	return false
}

func (projection *Projection) removeLinks() error {
	var failures []error
	for index := len(projection.links) - 1; index >= 0; index-- {
		link := &projection.links[index]
		if !link.created {
			continue
		}
		target, err := os.Readlink(link.path)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				failures = append(failures, fmt.Errorf("inspect Task Workspace link during rollback: %w", err))
			} else {
				link.created = false
			}
		} else if target != link.target {
			failures = append(failures, fmt.Errorf("refuse to remove changed Task Workspace link %q", link.path))
		} else if err := os.Remove(link.path); err != nil {
			failures = append(failures, fmt.Errorf("remove Task Workspace link: %w", err))
		} else {
			link.created = false
		}
	}
	return errors.Join(failures...)
}

func replaceGeneratedSection(document, generated []byte) ([]byte, error) {
	start := []byte(GeneratedStart)
	end := []byte(GeneratedEnd)
	startCount := bytes.Count(document, start)
	endCount := bytes.Count(document, end)
	if startCount == 0 && endCount == 0 {
		result := append([]byte(nil), document...)
		if len(result) > 0 {
			if result[len(result)-1] != '\n' {
				result = append(result, '\n')
			}
			result = append(result, '\n')
		}
		result = append(result, generated...)
		result = append(result, '\n')
		return result, nil
	}
	if startCount != 1 || endCount != 1 {
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
