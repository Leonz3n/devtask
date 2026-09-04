package config

import (
	"fmt"
	"os"
	"path/filepath"
)

type Paths struct {
	ConfigDir  string
	ConfigFile string
	LockFile   string
	DataDir    string
	TasksDir   string
	Workspaces string
}

func ResolvePaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("resolve home directory: %w", err)
	}
	configHome := absoluteEnvironmentPath("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	dataHome := absoluteEnvironmentPath("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	configDir := filepath.Join(configHome, "devtask")
	dataDir := filepath.Join(dataHome, "devtask")
	return Paths{
		ConfigDir:  configDir,
		ConfigFile: filepath.Join(configDir, "config.yaml"),
		LockFile:   filepath.Join(configDir, "config.lock"),
		DataDir:    dataDir,
		TasksDir:   filepath.Join(dataDir, "tasks"),
		Workspaces: filepath.Join(dataDir, "workspaces"),
	}, nil
}

func (paths Paths) WithTaskWorkspaceRoot(configuration Config) Paths {
	if configuration.TaskWorkspaceRoot != "" {
		paths.Workspaces = filepath.Clean(configuration.TaskWorkspaceRoot)
	}
	return paths
}

func absoluteEnvironmentPath(name, fallback string) string {
	value := os.Getenv(name)
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return fallback
}
