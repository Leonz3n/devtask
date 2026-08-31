package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Leonz3n/devtask/internal/lock"
	"gopkg.in/yaml.v3"
)

var ErrConcurrentEdit = errors.New("configuration changed while it was being updated")

type RegisteredRepository struct {
	Alias string `json:"alias"`
	Path  string `json:"path"`
}

type RegistrationAction string

const (
	RegistrationCreated   RegistrationAction = "registered"
	RegistrationUpdated   RegistrationAction = "updated"
	RegistrationUnchanged RegistrationAction = "already registered"
)

type RegistrationResult struct {
	Repository RegisteredRepository
	Action     RegistrationAction
}

func RegisterRepository(paths Paths, alias, repositoryPath string, update bool) (RegistrationResult, error) {
	if err := ValidateRepositoryAlias(alias); err != nil {
		return RegistrationResult{}, err
	}
	configLock, err := lock.Acquire(paths.LockFile)
	if err != nil {
		if errors.Is(err, lock.ErrBusy) {
			return RegistrationResult{}, fmt.Errorf("config is busy: another devtask process holds %s", paths.LockFile)
		}
		return RegistrationResult{}, err
	}
	defer configLock.Close()

	original, err := os.ReadFile(paths.ConfigFile)
	if err != nil {
		return RegistrationResult{}, fmt.Errorf("read configuration: %w", err)
	}
	configuration, err := decode(original)
	if err != nil {
		return RegistrationResult{}, err
	}
	for existingAlias, repository := range configuration.Repositories {
		if !strings.EqualFold(existingAlias, alias) {
			continue
		}
		registered := RegisteredRepository{Alias: existingAlias, Path: repository.Path}
		if repository.Path == repositoryPath {
			return RegistrationResult{Repository: registered, Action: RegistrationUnchanged}, nil
		}
		if !update {
			return RegistrationResult{}, invalid("repository alias %q is already registered at %q; use --update to change it", existingAlias, repository.Path)
		}
		contents, err := replaceRepositoryPath(original, existingAlias, repositoryPath)
		if err != nil {
			return RegistrationResult{}, err
		}
		if err := writeIfUnchanged(paths.ConfigFile, original, contents); err != nil {
			return RegistrationResult{}, err
		}
		return RegistrationResult{Repository: RegisteredRepository{Alias: existingAlias, Path: repositoryPath}, Action: RegistrationUpdated}, nil
	}

	contents, err := addRepository(original, alias, repositoryPath)
	if err != nil {
		return RegistrationResult{}, err
	}
	if err := writeIfUnchanged(paths.ConfigFile, original, contents); err != nil {
		return RegistrationResult{}, err
	}
	return RegistrationResult{Repository: RegisteredRepository{Alias: alias, Path: repositoryPath}, Action: RegistrationCreated}, nil
}

func ListRepositories(path string) ([]RegisteredRepository, error) {
	configuration, err := Load(path)
	if err != nil {
		return nil, err
	}
	repositories := make([]RegisteredRepository, 0, len(configuration.Repositories))
	for alias, repository := range configuration.Repositories {
		canonicalPath, err := filepath.EvalSymlinks(repository.Path)
		if err != nil {
			return nil, fmt.Errorf("resolve Main Checkout for repository %q at %q: %w", alias, repository.Path, err)
		}
		absolutePath, err := filepath.Abs(canonicalPath)
		if err != nil {
			return nil, fmt.Errorf("resolve Main Checkout for repository %q at %q: %w", alias, repository.Path, err)
		}
		repositories = append(repositories, RegisteredRepository{Alias: alias, Path: filepath.Clean(absolutePath)})
	}
	sort.Slice(repositories, func(i, j int) bool {
		return strings.ToLower(repositories[i].Alias) < strings.ToLower(repositories[j].Alias)
	})
	return repositories, nil
}

func addRepository(original []byte, alias, path string) ([]byte, error) {
	document, repositories, err := configurationNodes(original)
	if err != nil {
		return nil, err
	}
	repositories.Content = append(repositories.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: alias},
		repositoryNode(path),
	)
	return encodeAndValidate(document)
}

func replaceRepositoryPath(original []byte, alias, path string) ([]byte, error) {
	document, repositories, err := configurationNodes(original)
	if err != nil {
		return nil, err
	}
	for index := 0; index < len(repositories.Content); index += 2 {
		if repositories.Content[index].Value != alias {
			continue
		}
		value := repositories.Content[index+1]
		for field := 0; field < len(value.Content); field += 2 {
			if value.Content[field].Value == "path" {
				value.Content[field+1].Value = path
				value.Content[field+1].Tag = "!!str"
				return encodeAndValidate(document)
			}
		}
		value.Content = append([]*yaml.Node{
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "path"},
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: path},
		}, value.Content...)
		return encodeAndValidate(document)
	}
	return nil, fmt.Errorf("repository alias %q disappeared from configuration", alias)
}

func repositoryNode(path string) *yaml.Node {
	return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: "path"},
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: path},
	}}
}

func configurationNodes(contents []byte) (*yaml.Node, *yaml.Node, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(contents, &document); err != nil {
		return nil, nil, invalid("decode configuration: %v", err)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, nil, invalid("configuration root must be a mapping")
	}
	root := document.Content[0]
	for index := 0; index < len(root.Content); index += 2 {
		if root.Content[index].Value == "repositories" {
			if root.Content[index+1].Kind != yaml.MappingNode {
				return nil, nil, invalid("repositories must be a mapping")
			}
			return &document, root.Content[index+1], nil
		}
	}
	repositories := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	root.Content = append(root.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "repositories"},
		repositories,
	)
	return &document, repositories, nil
}

func encodeAndValidate(document *yaml.Node) ([]byte, error) {
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		return nil, fmt.Errorf("encode configuration: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("encode configuration: %w", err)
	}
	if _, err := decode(output.Bytes()); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func writeIfUnchanged(path string, original, contents []byte) error {
	if err := writeAtomicIfUnchanged(path, original, contents, 0o600); err != nil {
		if errors.Is(err, ErrConcurrentEdit) {
			return fmt.Errorf("%w; retry the command", err)
		}
		return fmt.Errorf("update configuration: %w", err)
	}
	return nil
}
