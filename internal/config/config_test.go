package config

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadMissingConfigurationExplainsHowToInitialize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	_, err := Load(path)

	if !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("Load error = %v, want ErrNotInitialized", err)
	}
	for _, expected := range []string{path, "run 'devtask init'"} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("Load error = %q, want it to contain %q", err, expected)
		}
	}
}

func TestDecodeRejectsRelativeAgentLauncherExecutablePath(t *testing.T) {
	_, err := decode([]byte("schema_version: 1\nagents:\n  pi:\n    command: ./bin/pi\n"))

	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "one executable name or absolute path") {
		t.Fatalf("decode error = %v, want relative Agent Launcher path validation", err)
	}
}

func TestDecodeAcceptsAbsoluteTaskWorkspaceRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "devtasks")

	configuration, err := decode([]byte("schema_version: 1\ntask_workspace_root: " + root + "\n"))

	if err != nil {
		t.Fatal(err)
	}
	if configuration.TaskWorkspaceRoot != root {
		t.Fatalf("TaskWorkspaceRoot = %q, want %q", configuration.TaskWorkspaceRoot, root)
	}
}

func TestDecodeRejectsRelativeTaskWorkspaceRoot(t *testing.T) {
	_, err := decode([]byte("schema_version: 1\ntask_workspace_root: visible-workspaces\n"))

	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "task_workspace_root must be an absolute path") {
		t.Fatalf("decode error = %v, want absolute Task Workspace root validation", err)
	}
}
