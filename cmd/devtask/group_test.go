package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Leonz3n/devtask/internal/config"
)

func TestNewGroupCreatesOrderedConcreteAttachments(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	repositories := map[string]string{
		"api":    createCommittedRepository(t, "api"),
		"worker": createCommittedRepository(t, "worker"),
		"web":    createCommittedRepository(t, "web"),
	}
	for _, alias := range []string{"api", "worker", "web"} {
		repository := repositories[alias]
		if err := os.WriteFile(filepath.Join(repository, ".gitignore"), []byte(".env\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitRun(t, "-C", repository, "add", ".gitignore")
		gitRun(t, "-C", repository, "commit", "-m", "ignore local environment")
		if err := os.WriteFile(filepath.Join(repository, ".env"), []byte(alias+" local\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if result := environment.run(t, "repo", "add", alias, repository); result.code != 0 {
			t.Fatalf("repo add %s failed: %s", alias, result.stderr)
		}
	}
	updateConfiguration(t, environment, func(configuration *config.Config) {
		for alias, repository := range configuration.Repositories {
			repository.SharedPaths = []string{".env"}
			configuration.Repositories[alias] = repository
		}
		configuration.Groups["Delivery"] = []string{"worker", "api", "WORKER"}
	})

	result := environment.run(t, "new", "rollout", "--group", "delivery", "--exclude", "API", "--exclude", "api", "--add", "web", "--add", "Worker", "--add", "api", "--no-fetch")

	if result.code != 0 {
		t.Fatalf("new --group failed: code=%d stdout=%q stderr=%q", result.code, result.stdout, result.stderr)
	}
	metadata := readPersistedTask(t, environment, "rollout")
	gotAliases := make([]string, len(metadata.Attachments))
	for index, attachment := range metadata.Attachments {
		gotAliases[index] = attachment.Alias
		if attachment.Order != index {
			t.Fatalf("Repository Attachment %s order = %d, want %d", attachment.Alias, attachment.Order, index)
		}
		worktree := filepath.Join(repositories[attachment.Alias], ".worktrees", "rollout")
		if branch := gitRun(t, "-C", worktree, "branch", "--show-current"); branch != "feat/rollout" {
			t.Fatalf("%s Task Worktree branch = %q", attachment.Alias, branch)
		}
		if contents, err := os.ReadFile(filepath.Join(worktree, ".env")); err != nil || string(contents) != attachment.Alias+" local\n" {
			t.Fatalf("%s Shared Local Path contents=%q error=%v", attachment.Alias, contents, err)
		}
		link := filepath.Join(environment.dataHome, "devtask", "workspaces", "rollout", attachment.Alias)
		if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s Task Workspace link: info=%v error=%v", attachment.Alias, info, err)
		}
	}
	if want := []string{"worker", "web", "api"}; !reflect.DeepEqual(gotAliases, want) {
		t.Fatalf("Repository Attachment aliases = %#v, want %#v", gotAliases, want)
	}
	agents, err := os.ReadFile(filepath.Join(environment.dataHome, "devtask", "workspaces", "rollout", "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	workerAt := bytes.Index(agents, []byte("- `worker`:"))
	webAt := bytes.Index(agents, []byte("- `web`:"))
	apiAt := bytes.Index(agents, []byte("- `api`:"))
	if workerAt < 0 || webAt < workerAt || apiAt < webAt {
		t.Fatalf("AGENTS.md Repository Attachment order is wrong:\n%s", agents)
	}

	updateConfiguration(t, environment, func(configuration *config.Config) {
		configuration.Groups["Delivery"] = []string{"api"}
	})
	afterGroupEdit := readPersistedTask(t, environment, "rollout")
	if !reflect.DeepEqual(afterGroupEdit.Attachments, metadata.Attachments) {
		t.Fatalf("later Repository Group edit changed Task snapshot: before=%#v after=%#v", metadata.Attachments, afterGroupEdit.Attachments)
	}
}

func TestNewGroupRejectsInvalidSelectionsBeforeTaskCreation(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*config.Config)
		arguments []string
		wantError string
	}{
		{
			name:      "unknown group",
			configure: func(configuration *config.Config) {},
			arguments: []string{"--group", "missing"},
			wantError: "unknown Repository Group",
		},
		{
			name: "unknown addition",
			configure: func(configuration *config.Config) {
				configuration.Groups["delivery"] = []string{"service"}
			},
			arguments: []string{"--group", "delivery", "--add", "missing"},
			wantError: "unknown Repository Alias",
		},
		{
			name: "group references unknown alias",
			configure: func(configuration *config.Config) {
				configuration.Groups["delivery"] = []string{"missing"}
			},
			arguments: []string{"--group", "delivery"},
			wantError: "references unknown repository alias",
		},
		{
			name: "ineffective exclusion",
			configure: func(configuration *config.Config) {
				configuration.Groups["delivery"] = []string{"service"}
			},
			arguments: []string{"--group", "delivery", "--exclude", "other"},
			wantError: "is not a member",
		},
		{
			name: "case insensitive group name conflict",
			configure: func(configuration *config.Config) {
				configuration.Groups["Delivery"] = []string{"service"}
				configuration.Groups["delivery"] = []string{"service"}
			},
			arguments: []string{"--group", "delivery"},
			wantError: "conflict case-insensitively",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment := initializedCLIEnvironment(t)
			repository := createCommittedRepository(t, "service")
			if result := environment.run(t, "repo", "add", "service", repository); result.code != 0 {
				t.Fatalf("repo add failed: %s", result.stderr)
			}
			updateConfiguration(t, environment, test.configure)

			arguments := append([]string{"new", "rollout"}, test.arguments...)
			result := environment.run(t, arguments...)

			if result.code != 2 || !strings.Contains(result.stderr, test.wantError) {
				t.Fatalf("invalid grouped new result: code=%d stderr=%q, want %q", result.code, result.stderr, test.wantError)
			}
			assertGroupedTaskAbsent(t, environment, "rollout")
			if branch := gitRun(t, "-C", repository, "branch", "--list", "feat/rollout"); branch != "" {
				t.Fatalf("invalid selection created Task Branch Name %q", branch)
			}
		})
	}

	environment := initializedCLIEnvironment(t)
	for _, arguments := range [][]string{
		{"new", "excluded", "--exclude", "service"},
		{"new", "added", "--add", "service"},
		{"new", "based", "--base", "main"},
	} {
		result := environment.run(t, arguments...)
		if result.code != 2 || !strings.Contains(result.stderr, "require --group") {
			t.Fatalf("ungrouped selection result for %v: code=%d stderr=%q", arguments, result.code, result.stderr)
		}
		assertGroupedTaskAbsent(t, environment, arguments[1])
	}
}

func assertGroupedTaskAbsent(t *testing.T, environment cliTestEnvironment, name string) {
	t.Helper()
	for _, path := range []string{
		filepath.Join(environment.dataHome, "devtask", "tasks", name+".yaml"),
		filepath.Join(environment.dataHome, "devtask", "workspaces", name),
	} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("group validation left %q: %v", path, err)
		}
	}
}

func TestNewGroupRemovesNewTaskWhenAttachmentCompensationSucceeds(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*testing.T, cliTestEnvironment, map[string]string) commandResult
	}{
		{
			name: "preflight failure",
			prepare: func(t *testing.T, environment cliTestEnvironment, repositories map[string]string) commandResult {
				collision := filepath.Join(repositories["second"], ".worktrees", "rollout")
				if err := os.MkdirAll(collision, 0o700); err != nil {
					t.Fatal(err)
				}
				return environment.run(t, "new", "rollout", "--group", "delivery", "--no-fetch")
			},
		},
		{
			name: "second worktree failure",
			prepare: func(t *testing.T, environment cliTestEnvironment, _ map[string]string) commandResult {
				return runFaultEnabledCLI(t, environment, map[string]string{
					"DEVTASK_TEST_FAIL_AFTER_WORKTREE_ALIAS": "second",
				}, "new", "rollout", "--group", "delivery", "--no-fetch")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment := initializedCLIEnvironment(t)
			repositories := map[string]string{
				"first":  createCommittedRepository(t, "first"),
				"second": createCommittedRepository(t, "second"),
			}
			for _, alias := range []string{"first", "second"} {
				if result := environment.run(t, "repo", "add", alias, repositories[alias]); result.code != 0 {
					t.Fatalf("repo add %s failed: %s", alias, result.stderr)
				}
			}
			updateConfiguration(t, environment, func(configuration *config.Config) {
				configuration.Groups["delivery"] = []string{"first", "second"}
			})

			result := test.prepare(t, environment, repositories)

			if result.code == 0 {
				t.Fatalf("grouped failure succeeded: stdout=%q", result.stdout)
			}
			assertGroupedTaskAbsent(t, environment, "rollout")
			for alias, repository := range repositories {
				worktree := filepath.Join(repository, ".worktrees", "rollout")
				if test.name == "preflight failure" && alias == "second" {
					if info, err := os.Lstat(worktree); err != nil || !info.IsDir() {
						t.Fatalf("compensation changed pre-existing collision: info=%v error=%v", info, err)
					}
					continue
				}
				if _, err := os.Lstat(worktree); !os.IsNotExist(err) {
					t.Fatalf("compensation left %s Task Worktree: %v", alias, err)
				}
				if branch := gitRun(t, "-C", repository, "branch", "--list", "feat/rollout"); branch != "" {
					t.Fatalf("compensation left %s Task Branch Name %q", alias, branch)
				}
			}
		})
	}
}

func TestNewGroupRetainsTruthfulIncompleteTaskWhenCompensationFails(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	repositories := map[string]string{
		"first":  createCommittedRepository(t, "first"),
		"second": createCommittedRepository(t, "second"),
	}
	for _, alias := range []string{"first", "second"} {
		if result := environment.run(t, "repo", "add", alias, repositories[alias]); result.code != 0 {
			t.Fatalf("repo add %s failed: %s", alias, result.stderr)
		}
	}
	updateConfiguration(t, environment, func(configuration *config.Config) {
		configuration.Groups["delivery"] = []string{"first", "second"}
	})

	binary := devtaskBinaryWithTags(t, "devtask_test")
	signalPath := filepath.Join(t.TempDir(), "group-projection")
	command := exec.Command(binary, "new", "rollout", "--group", "delivery", "--no-fetch")
	command.Dir = environment.home
	command.Env = append(filteredEnvironment(os.Environ()),
		"HOME="+environment.home,
		"XDG_CONFIG_HOME="+environment.configHome,
		"XDG_DATA_HOME="+environment.dataHome,
		"DEVTASK_TEST_PAUSE_AFTER_PROJECTION="+signalPath,
		"DEVTASK_TEST_FAIL_AFTER_PROJECTION=1",
	)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(signalPath + ".ready"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("grouped new did not reach projection boundary")
		}
		time.Sleep(time.Millisecond)
	}
	workspace := filepath.Join(environment.dataHome, "devtask", "workspaces", "rollout")
	secondLink := filepath.Join(workspace, "second")
	if err := os.Remove(secondLink); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondLink, []byte("user replacement\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(signalPath+".continue", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := command.Wait()

	if err == nil {
		t.Fatal("fault-enabled grouped new succeeded")
	}
	if exitError, ok := err.(*exec.ExitError); !ok || exitError.ExitCode() != 1 {
		t.Fatalf("grouped new error = %v, want exit 1; stderr: %s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "incomplete") {
		t.Fatalf("stderr = %q, want incomplete recovery diagnostic", stderr.String())
	}
	metadata := readPersistedTask(t, environment, "rollout")
	if metadata.State != "incomplete" || metadata.Incomplete == nil || metadata.Incomplete.Operation != "create_group" {
		t.Fatalf("grouped incomplete Task = %#v", metadata)
	}
	if len(metadata.Attachments) != 1 || metadata.Attachments[0].Alias != "second" || metadata.Attachments[0].State != "incomplete" {
		t.Fatalf("grouped residual Repository Attachments = %#v", metadata.Attachments)
	}
	if !strings.Contains(strings.Join(metadata.Attachments[0].ResidualObjects, "\n"), secondLink) {
		t.Fatalf("grouped residuals = %#v, want %q", metadata.Attachments[0].ResidualObjects, secondLink)
	}
	if contents, err := os.ReadFile(secondLink); err != nil || string(contents) != "user replacement\n" {
		t.Fatalf("compensation changed user replacement: contents=%q error=%v", contents, err)
	}
	status := environment.run(t, "status", "rollout")
	if status.code != 0 || !strings.Contains(status.stdout, "Incomplete operation: create_group") || !strings.Contains(status.stdout, secondLink) {
		t.Fatalf("status did not expose grouped recovery state: code=%d stdout=%q stderr=%q", status.code, status.stdout, status.stderr)
	}
}
