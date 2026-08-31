package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

const SchemaVersion = 1

var ErrNotInitialized = errors.New("devtask is not initialized")

type Config struct {
	SchemaVersion int                         `yaml:"schema_version"`
	Defaults      Defaults                    `yaml:"defaults"`
	Agents        Agents                      `yaml:"agents"`
	Repositories  map[string]RepositoryConfig `yaml:"repositories"`
	Groups        map[string][]string         `yaml:"groups"`
}

type Defaults struct {
	BaseBranch    string `yaml:"base_branch"`
	BranchPattern string `yaml:"branch_pattern"`
	Remote        string `yaml:"remote"`
	Fetch         bool   `yaml:"fetch"`
}

type Agents struct {
	Pi     AgentConfig `yaml:"pi"`
	Claude AgentConfig `yaml:"claude"`
	Codex  AgentConfig `yaml:"codex"`
}

type AgentConfig struct {
	Command string `yaml:"command"`
}

type RepositoryConfig struct {
	Path        string   `yaml:"path"`
	BaseBranch  string   `yaml:"base_branch,omitempty"`
	Remote      string   `yaml:"remote,omitempty"`
	Fetch       *bool    `yaml:"fetch,omitempty"`
	SharedPaths []string `yaml:"shared_paths,omitempty"`
}

func Default() Config {
	return Config{
		Defaults: Defaults{
			BaseBranch:    "main",
			BranchPattern: "feat/{{.Task}}",
			Remote:        "origin",
			Fetch:         true,
		},
		Agents: Agents{
			Pi:     AgentConfig{Command: "pi"},
			Claude: AgentConfig{Command: "claude"},
			Codex:  AgentConfig{Command: "codex"},
		},
		Repositories: map[string]RepositoryConfig{},
		Groups:       map[string][]string{},
	}
}

func Load(path string) (Config, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("%w: configuration %q is missing; run 'devtask init'", ErrNotInitialized, path)
	}
	if err != nil {
		return Config{}, fmt.Errorf("open configuration: %w", err)
	}
	defer file.Close()

	configuration := Default()
	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	if err := decoder.Decode(&configuration); err != nil {
		return Config{}, invalid("decode configuration: %v", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Config{}, invalid("configuration must contain exactly one YAML document")
		}
		return Config{}, invalid("decode configuration: %v", err)
	}
	if err := configuration.validate(); err != nil {
		return Config{}, err
	}
	return configuration, nil
}

func (configuration Config) validate() error {
	if configuration.SchemaVersion == 0 {
		return invalid("schema_version is required")
	}
	if configuration.SchemaVersion != SchemaVersion {
		return invalid("unsupported schema_version %d; supported version is %d", configuration.SchemaVersion, SchemaVersion)
	}
	if strings.TrimSpace(configuration.Defaults.BaseBranch) == "" {
		return invalid("defaults.base_branch must not be empty")
	}
	if strings.TrimSpace(configuration.Defaults.BranchPattern) == "" {
		return invalid("defaults.branch_pattern must not be empty")
	}
	branchTemplate, err := template.New("branch_pattern").Option("missingkey=error").Parse(configuration.Defaults.BranchPattern)
	if err != nil {
		return invalid("defaults.branch_pattern is invalid: %v", err)
	}
	var rendered strings.Builder
	if err := branchTemplate.Execute(&rendered, struct{ Task string }{Task: "example"}); err != nil {
		return invalid("defaults.branch_pattern is invalid: %v", err)
	}
	if strings.TrimSpace(rendered.String()) == "" {
		return invalid("defaults.branch_pattern must render a non-empty Task Branch Name")
	}
	for name, agent := range map[string]AgentConfig{
		"pi": configuration.Agents.Pi, "claude": configuration.Agents.Claude, "codex": configuration.Agents.Codex,
	} {
		if strings.TrimSpace(agent.Command) == "" {
			return invalid("agents.%s.command must not be empty", name)
		}
		if strings.ContainsRune(agent.Command, '\x00') || strings.ContainsAny(agent.Command, "\r\n") {
			return invalid("agents.%s.command must be one executable name or path", name)
		}
	}
	for alias, repository := range configuration.Repositories {
		if strings.TrimSpace(alias) == "" {
			return invalid("repository alias must not be empty")
		}
		if !filepath.IsAbs(repository.Path) {
			return invalid("repository %q path must be absolute", alias)
		}
	}
	return nil
}

func invalid(format string, arguments ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, arguments...))
}
