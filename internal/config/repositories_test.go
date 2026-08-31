package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegisterRepositoryReportsActionsAndPreservesComments(t *testing.T) {
	directory := t.TempDir()
	first := filepath.Join(directory, "first")
	second := filepath.Join(directory, "second")
	for _, path := range []string{first, second} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	paths := Paths{
		ConfigFile: filepath.Join(directory, "config.yaml"),
		LockFile:   filepath.Join(directory, "config.lock"),
	}
	original := "schema_version: 1\nrepositories:\n  # preserve repository context\n  Service:\n    path: " + first + "\n"
	if err := os.WriteFile(paths.ConfigFile, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	unchanged, err := RegisterRepository(paths, "service", first, false)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Action != RegistrationUnchanged {
		t.Fatalf("idempotent action = %q, want %q", unchanged.Action, RegistrationUnchanged)
	}
	updated, err := RegisterRepository(paths, "SERVICE", second, true)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Action != RegistrationUpdated || updated.Repository.Alias != "Service" {
		t.Fatalf("update result = %#v", updated)
	}
	created, err := RegisterRepository(paths, "other", first, false)
	if err != nil {
		t.Fatal(err)
	}
	if created.Action != RegistrationCreated {
		t.Fatalf("create action = %q, want %q", created.Action, RegistrationCreated)
	}
	contents, err := os.ReadFile(paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "# preserve repository context") {
		t.Fatalf("update lost YAML comment:\n%s", contents)
	}
}

func TestListRepositoriesCanonicalizesHumanEditedPath(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configuration := "schema_version: 1\nrepositories:\n  service:\n    path: " + filepath.Join(nested, "..") + "\n"
	if err := os.WriteFile(configPath, []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}

	repositories, err := ListRepositories(configPath)

	if err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(repositories) != 1 || repositories[0].Path != canonicalRoot {
		t.Fatalf("ListRepositories() = %#v, want canonical path %q", repositories, canonicalRoot)
	}
}

func TestAtomicConfigurationUpdateRestoresConcurrentEdit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	original := []byte("schema_version: 1\n")
	externalEdit := []byte("schema_version: 1\n# external edit\n")
	if err := os.WriteFile(path, externalEdit, 0o600); err != nil {
		t.Fatal(err)
	}

	err := writeAtomicIfUnchanged(path, original, []byte("schema_version: 1\nrepositories: {}\n"), 0o600)

	if !errors.Is(err, ErrConcurrentEdit) {
		t.Fatalf("writeAtomicIfUnchanged() error = %v, want ErrConcurrentEdit", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != string(externalEdit) {
		t.Fatalf("configuration = %q, want external edit %q", contents, externalEdit)
	}
}
