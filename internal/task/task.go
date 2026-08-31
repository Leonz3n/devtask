package task

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Leonz3n/devtask/internal/config"
	gitcmd "github.com/Leonz3n/devtask/internal/git"
	"github.com/Leonz3n/devtask/internal/lock"
	"github.com/Leonz3n/devtask/internal/workspace"
	"gopkg.in/yaml.v3"
)

const SchemaVersion = 1

var (
	ErrInvalid = errors.New("invalid Task")
	validName  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

type State string

const StateReady State = "ready"

type RepositoryAttachment struct{}

type Metadata struct {
	SchemaVersion  int                     `yaml:"schema_version"`
	Name           string                  `yaml:"name"`
	TaskBranchName string                  `yaml:"task_branch_name"`
	CreatedAt      time.Time               `yaml:"created_at"`
	State          State                   `yaml:"state"`
	ContextFiles   []workspace.ContextFile `yaml:"context_files"`
	Attachments    []RepositoryAttachment  `yaml:"attachments"`
}

type Summary struct {
	Name            string    `json:"name"`
	RepositoryCount int       `json:"repository_count"`
	CreatedAt       time.Time `json:"created_at"`
}

func Create(paths config.Paths, configuration config.Config, name string, branchOverride *string) (Metadata, error) {
	if err := ValidateName(name); err != nil {
		return Metadata{}, err
	}
	taskLock, err := lock.Acquire(lockPath(paths, name))
	if err != nil {
		if errors.Is(err, lock.ErrBusy) {
			return Metadata{}, fmt.Errorf("Task %q is busy: another devtask process holds its lock", name)
		}
		return Metadata{}, err
	}
	defer taskLock.Close()

	if err := ensureAvailable(paths, name); err != nil {
		return Metadata{}, err
	}
	branchName := ""
	if branchOverride == nil {
		branchName, err = config.RenderTaskBranchName(configuration.Defaults.BranchPattern, name)
		if err != nil {
			return Metadata{}, err
		}
	} else {
		branchName = *branchOverride
	}
	if err := gitcmd.ValidateBranchName(branchName); err != nil {
		return Metadata{}, invalid("%v", err)
	}

	prepared, err := workspace.Prepare(paths.Workspaces, name, branchName)
	if err != nil {
		return Metadata{}, invalid("%v", err)
	}
	metadata := Metadata{
		SchemaVersion:  SchemaVersion,
		Name:           name,
		TaskBranchName: branchName,
		CreatedAt:      time.Now().UTC(),
		State:          StateReady,
		ContextFiles:   prepared.ContextFiles,
		Attachments:    make([]RepositoryAttachment, 0),
	}
	contents, err := yaml.Marshal(metadata)
	if err != nil {
		_ = prepared.Abort()
		return Metadata{}, fmt.Errorf("encode Task metadata: %w", err)
	}
	if err := prepared.Commit(); err != nil {
		_ = prepared.Abort()
		return Metadata{}, err
	}
	metadataPath := filepath.Join(paths.TasksDir, name+".yaml")
	published, err := writeAtomicNew(metadataPath, contents)
	if err != nil {
		var cleanupErrors []error
		if published {
			cleanupErrors = append(cleanupErrors, removePublishedMetadata(metadataPath))
		}
		cleanupErrors = append(cleanupErrors, prepared.Abort())
		cleanupError := errors.Join(cleanupErrors...)
		if cleanupError != nil {
			return Metadata{}, fmt.Errorf("write Task metadata: %v; roll back Task creation: %w", err, cleanupError)
		}
		if errors.Is(err, os.ErrExist) {
			return Metadata{}, invalid("Task metadata collision at %q", metadataPath)
		}
		return Metadata{}, fmt.Errorf("write Task metadata: %w", err)
	}
	return metadata, nil
}

func List(paths config.Paths) ([]Summary, error) {
	entries, err := os.ReadDir(paths.TasksDir)
	if err != nil {
		return nil, fmt.Errorf("list Tasks: %w", err)
	}
	tasks := make([]Summary, 0)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		metadata, err := load(filepath.Join(paths.TasksDir, entry.Name()))
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, Summary{Name: metadata.Name, RepositoryCount: len(metadata.Attachments), CreatedAt: metadata.CreatedAt})
	}
	sort.Slice(tasks, func(i, j int) bool { return strings.ToLower(tasks[i].Name) < strings.ToLower(tasks[j].Name) })
	return tasks, nil
}

func ValidateName(name string) error {
	if !validName.MatchString(name) {
		return invalid("invalid Task name %q; expected [A-Za-z0-9][A-Za-z0-9._-]*", name)
	}
	return nil
}

func ensureAvailable(paths config.Paths, name string) error {
	entries, err := os.ReadDir(paths.TasksDir)
	if err != nil {
		return fmt.Errorf("inspect Tasks: %w", err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		existingName := strings.TrimSuffix(entry.Name(), ".yaml")
		if strings.EqualFold(existingName, name) {
			return invalid("Task %q already exists", existingName)
		}
	}
	workspaces, err := os.ReadDir(paths.Workspaces)
	if err != nil {
		return fmt.Errorf("inspect Task Workspaces: %w", err)
	}
	for _, entry := range workspaces {
		if strings.EqualFold(entry.Name(), name) {
			return invalid("Task Workspace collision for %q at %q", name, filepath.Join(paths.Workspaces, entry.Name()))
		}
	}
	return nil
}

func load(path string) (Metadata, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return Metadata{}, fmt.Errorf("read Task metadata %q: %w", path, err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	decoder.KnownFields(true)
	var metadata Metadata
	if err := decoder.Decode(&metadata); err != nil {
		return Metadata{}, invalid("decode Task metadata %q: %v", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Metadata{}, invalid("Task metadata %q must contain exactly one YAML document", path)
	}
	if metadata.SchemaVersion != SchemaVersion {
		return Metadata{}, invalid("unsupported Task schema_version %d in %q", metadata.SchemaVersion, path)
	}
	if err := ValidateName(metadata.Name); err != nil {
		return Metadata{}, err
	}
	if metadata.State != StateReady {
		return Metadata{}, invalid("unsupported Task state %q in %q", metadata.State, path)
	}
	return metadata, nil
}

func lockPath(paths config.Paths, name string) string {
	return filepath.Join(paths.TasksDir, "."+strings.ToLower(name)+".lock")
}

func writeAtomicNew(path string, contents []byte) (bool, error) {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".task.yaml-*")
	if err != nil {
		return false, err
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		if temporaryPath != "" {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return false, err
	}
	if _, err := temporary.Write(contents); err != nil {
		return false, err
	}
	if err := temporary.Sync(); err != nil {
		return false, err
	}
	if err := temporary.Close(); err != nil {
		return false, err
	}
	if err := os.Link(temporaryPath, path); err != nil {
		return false, err
	}
	if err := os.Remove(temporaryPath); err != nil {
		return true, err
	}
	temporaryPath = ""
	return true, syncDirectory(directory)
}

func removePublishedMetadata(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func invalid(format string, arguments ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, arguments...))
}
