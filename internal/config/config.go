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
	"text/template/parse"

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
	return decode(contents)
}

func decode(contents []byte) (Config, error) {
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

func ValidateRepositoryAlias(alias string) error {
	if !validName.MatchString(alias) {
		return invalid("invalid repository alias %q; expected [A-Za-z0-9][A-Za-z0-9._-]*", alias)
	}
	if _, reserved := reservedRepositoryAliases[strings.ToLower(alias)]; reserved {
		return invalid("repository alias %q is a reserved Task Context File name", alias)
	}
	return nil
}

func RenderTaskBranchName(pattern, taskName string) (string, error) {
	branchTemplate, err := template.New("branch_pattern").Option("missingkey=error").Parse(pattern)
	if err != nil {
		return "", invalid("defaults.branch_pattern is invalid: %v", err)
	}
	if err := validateBranchTemplate(branchTemplate.Tree.Root); err != nil {
		return "", err
	}
	var rendered strings.Builder
	if err := branchTemplate.Execute(&rendered, struct{ Task string }{Task: taskName}); err != nil {
		return "", invalid("defaults.branch_pattern is invalid: %v", err)
	}
	if rendered.Len() == 0 {
		return "", invalid("defaults.branch_pattern must render a non-empty Task Branch Name")
	}
	return rendered.String(), nil
}

func validateBranchTemplate(root *parse.ListNode) error {
	for _, node := range root.Nodes {
		switch node := node.(type) {
		case *parse.TextNode:
			continue
		case *parse.ActionNode:
			pipe := node.Pipe
			if len(pipe.Decl) == 0 && !pipe.IsAssign && len(pipe.Cmds) == 1 && len(pipe.Cmds[0].Args) == 1 {
				if field, ok := pipe.Cmds[0].Args[0].(*parse.FieldNode); ok && len(field.Ident) == 1 && field.Ident[0] == "Task" {
					continue
				}
			}
		}
		return invalid("defaults.branch_pattern may contain only text and {{.Task}}")
	}
	return nil
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
	rendered, err := RenderTaskBranchName(configuration.Defaults.BranchPattern, "example")
	if err != nil {
		return err
	}
	if strings.TrimSpace(rendered) == "" {
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
		if !filepath.IsAbs(agent.Command) && filepath.Base(agent.Command) != agent.Command {
			return invalid("agents.%s.command must be one executable name or absolute path", name)
		}
	}
	repositoriesByFoldedAlias := make(map[string]string, len(configuration.Repositories))
	for alias, repository := range configuration.Repositories {
		if err := ValidateRepositoryAlias(alias); err != nil {
			return err
		}
		foldedAlias := strings.ToLower(alias)
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
