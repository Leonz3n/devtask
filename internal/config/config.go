package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

const SchemaVersion = 1

var ErrNotInitialized = errors.New("devtask is not initialized")

var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

var reservedRepositoryAliases = map[string]struct{}{
	"agents.md":  {},
	"context.md": {},
	"spec.md":    {},
	"task.md":    {},
}

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
		SchemaVersion: SchemaVersion,
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
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("%w: configuration %q is missing; run 'devtask init'", ErrNotInitialized, path)
	}
	if err != nil {
		return Config{}, fmt.Errorf("read configuration: %w", err)
	}
	var header struct {
		SchemaVersion *int `yaml:"schema_version"`
	}
	if err := yaml.Unmarshal(contents, &header); err != nil {
		return Config{}, invalid("decode configuration: %v", err)
	}
	if header.SchemaVersion == nil {
		return Config{}, invalid("schema_version is required")
	}

	configuration := Default()
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
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
	repositoriesByFoldedAlias := make(map[string]string, len(configuration.Repositories))
	for alias, repository := range configuration.Repositories {
		if !validName.MatchString(alias) {
			return invalid("invalid repository alias %q; expected [A-Za-z0-9][A-Za-z0-9._-]*", alias)
		}
		foldedAlias := strings.ToLower(alias)
		if _, reserved := reservedRepositoryAliases[foldedAlias]; reserved {
			return invalid("repository alias %q is a reserved Task Context File name", alias)
		}
		if existing, duplicate := repositoriesByFoldedAlias[foldedAlias]; duplicate {
			return invalid("repository aliases %q and %q conflict case-insensitively", existing, alias)
		}
		repositoriesByFoldedAlias[foldedAlias] = alias
		if !filepath.IsAbs(repository.Path) {
			return invalid("repository %q path must be absolute", alias)
		}
		if repository.BaseBranch != "" && strings.TrimSpace(repository.BaseBranch) == "" {
			return invalid("repository %q base_branch must not be whitespace", alias)
		}
		if repository.Remote != "" && strings.TrimSpace(repository.Remote) == "" {
			return invalid("repository %q remote must not be whitespace", alias)
		}
		for _, sharedPath := range repository.SharedPaths {
			cleaned := filepath.Clean(sharedPath)
			if sharedPath == "" || filepath.IsAbs(sharedPath) || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
				return invalid("repository %q shared path %q must be relative and remain inside the Main Checkout", alias, sharedPath)
			}
		}
	}
	groupNames := make(map[string]string, len(configuration.Groups))
	for name, aliases := range configuration.Groups {
		if !validName.MatchString(name) {
			return invalid("invalid repository group name %q; expected [A-Za-z0-9][A-Za-z0-9._-]*", name)
		}
		foldedName := strings.ToLower(name)
		if existing, duplicate := groupNames[foldedName]; duplicate {
			return invalid("repository group names %q and %q conflict case-insensitively", existing, name)
		}
		groupNames[foldedName] = name
		for _, alias := range aliases {
			if _, exists := repositoriesByFoldedAlias[strings.ToLower(alias)]; !exists {
				return invalid("repository group %q references unknown repository alias %q", name, alias)
			}
		}
	}
	return nil
}

func marshal(configuration Config) ([]byte, error) {
	contents, err := yaml.Marshal(configuration)
	if err != nil {
		return nil, fmt.Errorf("encode configuration: %w", err)
	}
	return contents, nil
}

func invalid(format string, arguments ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, arguments...))
}
