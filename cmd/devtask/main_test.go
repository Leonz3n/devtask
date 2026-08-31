package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"
)

type listedRepository struct {
	Alias string `json:"alias"`
	Path  string `json:"path"`
}

type listedTask struct {
	Name            string    `json:"name"`
	RepositoryCount int       `json:"repository_count"`
	CreatedAt       time.Time `json:"created_at"`
}

type persistedTask struct {
	SchemaVersion  int       `yaml:"schema_version"`
	Name           string    `yaml:"name"`
	TaskBranchName string    `yaml:"task_branch_name"`
	CreatedAt      time.Time `yaml:"created_at"`
	State          string    `yaml:"state"`
	ContextFiles   []struct {
		Path   string `yaml:"path"`
		SHA256 string `yaml:"sha256"`
	} `yaml:"context_files"`
	Attachments []any `yaml:"attachments"`
}

func devtaskBinary(t *testing.T) string {
	return devtaskBinaryWithTags(t, "")
}

func devtaskBinaryWithTags(t *testing.T, tags string) string {
	t.Helper()

	binary := filepath.Join(t.TempDir(), "devtask")
	arguments := []string{"build"}
	if tags != "" {
		arguments = append(arguments, "-tags", tags)
	}
	arguments = append(arguments, "-o", binary, ".")
	command := exec.Command("go", arguments...)
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test source")
	}
	command.Dir = filepath.Dir(filename)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("build devtask: %v", &commandError{err: err, output: string(output)})
	}
	return binary
}

type commandError struct {
	err    error
	output string
}

func (e *commandError) Error() string {
	return e.err.Error() + ": " + e.output
}

type commandResult struct {
	stdout string
	stderr string
	code   int
}

type cliTestEnvironment struct {
	home       string
	configHome string
	dataHome   string
}

func newCLITestEnvironment(t *testing.T) cliTestEnvironment {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatalf("create test home: %v", err)
	}
	return cliTestEnvironment{
		home:       home,
		configHome: filepath.Join(root, "config"),
		dataHome:   filepath.Join(root, "data"),
	}
}

func (environment cliTestEnvironment) run(t *testing.T, args ...string) commandResult {
	t.Helper()
	return environment.runWithXDG(t, environment.configHome, environment.dataHome, args...)
}

func (environment cliTestEnvironment) runWithXDG(t *testing.T, configHome, dataHome string, args ...string) commandResult {
	t.Helper()

	command := exec.Command(devtaskBinary(t), args...)
	command.Dir = environment.home
	command.Env = append(filteredEnvironment(os.Environ()),
		"HOME="+environment.home,
		"XDG_CONFIG_HOME="+configHome,
		"XDG_DATA_HOME="+dataHome,
	)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	code := 0
	if err != nil {
		var exitError *exec.ExitError
		if !strings.Contains(err.Error(), "exit status") || !asExitError(err, &exitError) {
			t.Fatalf("run devtask: %v", err)
		}
		code = exitError.ExitCode()
	}
	return commandResult{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

func asExitError(err error, target **exec.ExitError) bool {
	exitError, ok := err.(*exec.ExitError)
	if ok {
		*target = exitError
	}
	return ok
}

func filteredEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment))
	for _, value := range environment {
		if strings.HasPrefix(value, "HOME=") ||
			strings.HasPrefix(value, "XDG_CONFIG_HOME=") ||
			strings.HasPrefix(value, "XDG_DATA_HOME=") {
			continue
		}
		result = append(result, value)
	}
	return result
}

func TestHelpDoesNotInitializeState(t *testing.T) {
	environment := newCLITestEnvironment(t)

	result := environment.run(t, "--help")

	if result.code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", result.code, result.stderr)
	}
	if !strings.Contains(result.stdout, "Usage:") || !strings.Contains(result.stdout, "devtask init") {
		t.Fatalf("help output does not describe the CLI:\n%s", result.stdout)
	}
	for _, path := range []string{filepath.Join(environment.configHome, "devtask"), filepath.Join(environment.dataHome, "devtask")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("help created state path %q", path)
		}
	}
}

func TestUnknownCommandUsesValidationExitCode(t *testing.T) {
	environment := newCLITestEnvironment(t)

	result := environment.run(t, "does-not-exist")

	if result.code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr: %s", result.code, result.stderr)
	}
	if !strings.Contains(result.stderr, "unknown command") {
		t.Fatalf("stderr = %q, want unknown-command diagnostic", result.stderr)
	}
}

func TestInitCreatesVersionedStateInAbsoluteXDGLocations(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("devtask v1 supports macOS and Linux")
	}
	environment := newCLITestEnvironment(t)

	result := environment.run(t, "init")

	if result.code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", result.code, result.stderr)
	}
	if result.stderr != "" {
		t.Fatalf("stderr = %q, want empty", result.stderr)
	}
	configPath := filepath.Join(environment.configHome, "devtask", "config.yaml")
	assertFileMode(t, configPath, 0o600)
	assertDirectoryMode(t, filepath.Join(environment.configHome, "devtask"), 0o700)
	assertDirectoryMode(t, filepath.Join(environment.dataHome, "devtask"), 0o700)
	assertDirectoryMode(t, filepath.Join(environment.dataHome, "devtask", "tasks"), 0o700)
	assertDirectoryMode(t, filepath.Join(environment.dataHome, "devtask", "workspaces"), 0o700)
	contents, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(contents), "schema_version: 1\n") {
		t.Fatalf("config does not start with schema version 1:\n%s", contents)
	}
	if !strings.Contains(result.stdout, configPath) || !strings.Contains(result.stdout, filepath.Join(environment.dataHome, "devtask")) {
		t.Fatalf("init output does not identify persisted state:\n%s", result.stdout)
	}
}

func TestInitIsIdempotentAndPreservesExistingConfiguration(t *testing.T) {
	environment := newCLITestEnvironment(t)
	first := environment.run(t, "init")
	if first.code != 0 {
		t.Fatalf("first init failed: %s", first.stderr)
	}

	configPath := filepath.Join(environment.configHome, "devtask", "config.yaml")
	custom := "schema_version: 1\n\ndefaults:\n  base_branch: trunk\n"
	if err := os.WriteFile(configPath, []byte(custom), 0o600); err != nil {
		t.Fatal(err)
	}
	second := environment.run(t, "init")
	if second.code != 0 {
		t.Fatalf("second init failed: %s", second.stderr)
	}
	contents, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != custom {
		t.Fatalf("existing config was overwritten:\n%s", contents)
	}
}

func TestInitIgnoresRelativeXDGValues(t *testing.T) {
	environment := newCLITestEnvironment(t)

	result := environment.runWithXDG(t, "relative-config", "relative-data", "init")

	if result.code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", result.code, result.stderr)
	}
	assertFileMode(t, filepath.Join(environment.home, ".config", "devtask", "config.yaml"), 0o600)
	assertDirectoryMode(t, filepath.Join(environment.home, ".local", "share", "devtask"), 0o700)
	for _, path := range []string{filepath.Join(environment.home, "relative-config"), filepath.Join(environment.home, "relative-data")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("relative XDG value created path %q", path)
		}
	}
}

func TestInitRejectsInvalidExistingConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		message string
	}{
		{
			name:    "unknown root field",
			config:  "schema_version: 1\nunexpected: true\n",
			message: "field unexpected not found",
		},
		{
			name:    "unknown nested field",
			config:  "schema_version: 1\ndefaults:\n  mystery: value\n",
			message: "field mystery not found",
		},
		{
			name:    "unsupported schema",
			config:  "schema_version: 2\n",
			message: "unsupported schema_version 2",
		},
		{
			name:    "missing schema",
			config:  "defaults:\n  base_branch: main\n",
			message: "schema_version is required",
		},
		{
			name:    "empty base ref",
			config:  "schema_version: 1\ndefaults:\n  base_branch: \"\"\n",
			message: "defaults.base_branch must not be empty",
		},
		{
			name:    "unsupported branch template value",
			config:  "schema_version: 1\ndefaults:\n  branch_pattern: 'feat/{{.Unknown}}'\n",
			message: "defaults.branch_pattern",
		},
		{
			name:    "invalid repository alias",
			config:  "schema_version: 1\nrepositories:\n  'bad alias':\n    path: /tmp/repository\n",
			message: "invalid repository alias",
		},
		{
			name:    "reserved repository alias",
			config:  "schema_version: 1\nrepositories:\n  TASK.md:\n    path: /tmp/repository\n",
			message: "reserved Task Context File name",
		},
		{
			name:    "case insensitive repository alias conflict",
			config:  "schema_version: 1\nrepositories:\n  service:\n    path: /tmp/one\n  SERVICE:\n    path: /tmp/two\n",
			message: "conflict case-insensitively",
		},
		{
			name:    "unsafe shared local path",
			config:  "schema_version: 1\nrepositories:\n  service:\n    path: /tmp/repository\n    shared_paths: ['../secret']\n",
			message: "shared path",
		},
		{
			name:    "invalid repository group name",
			config:  "schema_version: 1\ngroups:\n  'bad group': []\n",
			message: "invalid repository group name",
		},
		{
			name:    "unknown repository group member",
			config:  "schema_version: 1\ngroups:\n  billing: [missing]\n",
			message: "references unknown repository alias",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment := newCLITestEnvironment(t)
			configDirectory := filepath.Join(environment.configHome, "devtask")
			if err := os.MkdirAll(configDirectory, 0o700); err != nil {
				t.Fatal(err)
			}
			configPath := filepath.Join(configDirectory, "config.yaml")
			if err := os.WriteFile(configPath, []byte(test.config), 0o600); err != nil {
				t.Fatal(err)
			}

			result := environment.run(t, "init")

			if result.code != 2 {
				t.Fatalf("exit code = %d, want 2; stderr: %s", result.code, result.stderr)
			}
			if !strings.Contains(result.stderr, test.message) {
				t.Fatalf("stderr = %q, want it to contain %q", result.stderr, test.message)
			}
			if _, err := os.Stat(filepath.Join(environment.dataHome, "devtask")); !os.IsNotExist(err) {
				t.Fatalf("invalid configuration created data state")
			}
		})
	}
}

func TestInitFailsClearlyWhenConfigurationLockIsBusy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("devtask v1 supports macOS and Linux")
	}
	environment := newCLITestEnvironment(t)
	configDirectory := filepath.Join(environment.configHome, "devtask")
	if err := os.MkdirAll(configDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(configDirectory, "config.lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatalf("hold config lock: %v", err)
	}

	result := environment.run(t, "init")

	if result.code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr: %s", result.code, result.stderr)
	}
	if !strings.Contains(result.stderr, "config is busy") {
		t.Fatalf("stderr = %q, want a clear busy-lock diagnostic", result.stderr)
	}
	if _, err := os.Stat(filepath.Join(configDirectory, "config.yaml")); !os.IsNotExist(err) {
		t.Fatal("busy init created configuration")
	}
}

func TestRepoAddFromNestedDirectoryAndList(t *testing.T) {
	environment := newCLITestEnvironment(t)
	initialize := environment.run(t, "init")
	if initialize.code != 0 {
		t.Fatalf("init failed: %s", initialize.stderr)
	}
	repository := filepath.Join(t.TempDir(), "invoice-service")
	gitRun(t, "init", repository)
	nested := filepath.Join(repository, "internal", "billing")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}

	add := environment.run(t, "repo", "add", "invoice-service", nested)

	if add.code != 0 {
		t.Fatalf("repo add exit code = %d, want 0; stderr: %s", add.code, add.stderr)
	}
	if add.stderr != "" {
		t.Fatalf("repo add stderr = %q, want empty", add.stderr)
	}
	if !strings.Contains(add.stdout, "invoice-service") || !strings.Contains(add.stdout, repository) {
		t.Fatalf("repo add output does not identify registration:\n%s", add.stdout)
	}

	human := environment.run(t, "repo", "list")
	if human.code != 0 {
		t.Fatalf("repo list failed: %s", human.stderr)
	}
	if !strings.Contains(human.stdout, "invoice-service") || !strings.Contains(human.stdout, repository) {
		t.Fatalf("repo list output does not contain registration:\n%s", human.stdout)
	}

	machine := environment.run(t, "repo", "list", "--json")
	if machine.code != 0 {
		t.Fatalf("repo list --json failed: %s", machine.stderr)
	}
	var repositories []listedRepository
	if err := json.Unmarshal([]byte(machine.stdout), &repositories); err != nil {
		t.Fatalf("decode repo list JSON: %v\n%s", err, machine.stdout)
	}
	canonicalRepository, err := filepath.EvalSymlinks(repository)
	if err != nil {
		t.Fatal(err)
	}
	want := []listedRepository{{Alias: "invoice-service", Path: canonicalRepository}}
	if len(repositories) != len(want) || repositories[0] != want[0] {
		t.Fatalf("repo list JSON = %#v, want %#v", repositories, want)
	}
}

func TestRepoAddFromMainCheckoutRootWithUnusualPath(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	mainCheckout := filepath.Join(t.TempDir(), "service\nwith-unicode-\u00e9")
	gitRun(t, "init", mainCheckout)

	result := environment.run(t, "repo", "add", "service", mainCheckout)

	if result.code != 0 {
		t.Fatalf("repo add failed: %s", result.stderr)
	}
	canonicalMainCheckout, err := filepath.EvalSymlinks(mainCheckout)
	if err != nil {
		t.Fatal(err)
	}
	listed := listRepositories(t, environment)
	if len(listed) != 1 || listed[0] != (listedRepository{Alias: "service", Path: canonicalMainCheckout}) {
		t.Fatalf("repo list = %#v, want Main Checkout %q", listed, canonicalMainCheckout)
	}
}

func TestRepoAddFromLinkedWorktreeRegistersMainCheckout(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	mainCheckout := filepath.Join(t.TempDir(), "main-checkout")
	gitRun(t, "init", mainCheckout)
	gitRun(t, "-C", mainCheckout, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "initial")
	linkedWorktree := filepath.Join(t.TempDir(), "linked-worktree")
	gitRun(t, "-C", mainCheckout, "worktree", "add", "--detach", linkedWorktree)
	insideLinkedWorktree := filepath.Join(linkedWorktree, "nested")
	if err := os.Mkdir(insideLinkedWorktree, 0o700); err != nil {
		t.Fatal(err)
	}

	result := environment.run(t, "repo", "add", "service", insideLinkedWorktree)

	if result.code != 0 {
		t.Fatalf("repo add failed: %s", result.stderr)
	}
	listed := listRepositories(t, environment)
	canonicalMainCheckout, err := filepath.EvalSymlinks(mainCheckout)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0] != (listedRepository{Alias: "service", Path: canonicalMainCheckout}) {
		t.Fatalf("repo list = %#v, want Main Checkout %q", listed, canonicalMainCheckout)
	}
}

func TestRepoAddRejectsInvalidRepositoryPaths(t *testing.T) {
	tests := []struct {
		name    string
		path    func(*testing.T) string
		message string
	}{
		{
			name: "missing path",
			path: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "missing")
			},
			message: "inspect repository path",
		},
		{
			name: "non Git directory",
			path: func(t *testing.T) string {
				return t.TempDir()
			},
			message: "not inside a Git repository",
		},
		{
			name: "bare repository",
			path: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "bare.git")
				gitRun(t, "init", "--bare", path)
				return path
			},
			message: "bare repository; register a Main Checkout",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment := initializedCLIEnvironment(t)

			result := environment.run(t, "repo", "add", "service", test.path(t))

			if result.code != 2 {
				t.Fatalf("exit code = %d, want 2; stderr: %s", result.code, result.stderr)
			}
			if !strings.Contains(result.stderr, test.message) {
				t.Fatalf("stderr = %q, want it to contain %q", result.stderr, test.message)
			}
			if listed := listRepositories(t, environment); len(listed) != 0 {
				t.Fatalf("invalid repository was registered: %#v", listed)
			}
		})
	}
}

func TestRepoAddValidatesPortableAndReservedAliases(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	repository := filepath.Join(t.TempDir(), "repository")
	gitRun(t, "init", repository)

	for _, alias := range []string{"bad alias", ".", "..", "-leading", "TASK.md", "context.MD"} {
		t.Run(alias, func(t *testing.T) {
			arguments := []string{"repo", "add"}
			if strings.HasPrefix(alias, "-") {
				arguments = append(arguments, "--")
			}
			result := environment.run(t, append(arguments, alias, repository)...)

			if result.code != 2 {
				t.Fatalf("exit code = %d, want 2; stderr: %s", result.code, result.stderr)
			}
			if !strings.Contains(result.stderr, "repository alias") {
				t.Fatalf("stderr = %q, want Repository Alias diagnostic", result.stderr)
			}
		})
	}
	if listed := listRepositories(t, environment); len(listed) != 0 {
		t.Fatalf("invalid aliases were registered: %#v", listed)
	}
}

func TestRepoAddIsIdempotentAndUpdatePreservesConfiguration(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	firstRepository := filepath.Join(t.TempDir(), "first")
	secondRepository := filepath.Join(t.TempDir(), "second")
	gitRun(t, "init", firstRepository)
	gitRun(t, "init", secondRepository)
	canonicalFirst, err := filepath.EvalSymlinks(firstRepository)
	if err != nil {
		t.Fatal(err)
	}
	canonicalSecond, err := filepath.EvalSymlinks(secondRepository)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(environment.configHome, "devtask", "config.yaml")
	configuration := fmt.Sprintf(`# keep this header comment
schema_version: 1
repositories:
  # keep repository comment
  Service:
    path: %q # keep path comment
    base_branch: trunk
groups:
  # keep group comment and ordering
  backend: [Service]
`, canonicalFirst)
	if err := os.WriteFile(configPath, []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}

	idempotent := environment.run(t, "repo", "add", "service", firstRepository)
	if idempotent.code != 0 {
		t.Fatalf("idempotent repo add failed: %s", idempotent.stderr)
	}
	if !strings.Contains(idempotent.stdout, "already registered Service") {
		t.Fatalf("idempotent output = %q", idempotent.stdout)
	}
	unchanged, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(unchanged) != configuration {
		t.Fatalf("idempotent registration rewrote configuration:\n%s", unchanged)
	}

	refused := environment.run(t, "repo", "add", "SERVICE", secondRepository)
	if refused.code != 2 {
		t.Fatalf("different path exit code = %d, want 2; stderr: %s", refused.code, refused.stderr)
	}
	if !strings.Contains(refused.stderr, "use --update") {
		t.Fatalf("refusal stderr = %q, want --update guidance", refused.stderr)
	}
	stillUnchanged, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(stillUnchanged) != configuration {
		t.Fatalf("refused registration rewrote configuration:\n%s", stillUnchanged)
	}

	updated := environment.run(t, "repo", "add", "service", secondRepository, "--update")
	if updated.code != 0 {
		t.Fatalf("repo add --update failed: %s", updated.stderr)
	}
	contents, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	updatedConfiguration := string(contents)
	for _, preserved := range []string{"# keep this header comment", "# keep repository comment", "# keep path comment", "# keep group comment and ordering", "base_branch: trunk"} {
		if !strings.Contains(updatedConfiguration, preserved) {
			t.Fatalf("updated configuration lost %q:\n%s", preserved, updatedConfiguration)
		}
	}
	if strings.Index(updatedConfiguration, "Service:") > strings.Index(updatedConfiguration, "groups:") {
		t.Fatalf("updated configuration changed unrelated key ordering:\n%s", updatedConfiguration)
	}
	listed := listRepositories(t, environment)
	if len(listed) != 1 || listed[0] != (listedRepository{Alias: "Service", Path: canonicalSecond}) {
		t.Fatalf("repo list after update = %#v", listed)
	}
}

func TestRepoAddDetectsConcurrentExternalConfigurationEdit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("devtask v1 supports macOS and Linux")
	}
	environment := initializedCLIEnvironment(t)
	repository := filepath.Join(t.TempDir(), "repository")
	gitRun(t, "init", repository)
	configPath := filepath.Join(environment.configHome, "devtask", "config.yaml")
	lockPath := filepath.Join(environment.configHome, "devtask", "config.lock")
	largeValidConfiguration := "schema_version: 1\nrepositories: {}\ngroups: {}\n" + strings.Repeat("# padding keeps YAML decoding active during the external edit\n", 100_000)
	if err := os.WriteFile(configPath, []byte(largeValidConfiguration), 0o600); err != nil {
		t.Fatal(err)
	}

	command := exec.Command(devtaskBinary(t), "repo", "add", "service", repository)
	command.Dir = environment.home
	command.Env = append(filteredEnvironment(os.Environ()),
		"HOME="+environment.home,
		"XDG_CONFIG_HOME="+environment.configHome,
		"XDG_DATA_HOME="+environment.dataHome,
	)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(10 * time.Second)
	observedBusy := false
	for time.Now().Before(deadline) {
		probe, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		err = unix.Flock(int(probe.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			_ = unix.Flock(int(probe.Fd()), unix.LOCK_UN)
			_ = probe.Close()
			time.Sleep(time.Millisecond)
			continue
		}
		_ = probe.Close()
		if err == unix.EWOULDBLOCK || err == unix.EAGAIN {
			observedBusy = true
			break
		}
		t.Fatalf("probe config lock: %v", err)
	}
	if !observedBusy {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatal("did not observe repo add holding the config lock")
	}
	// The lock is acquired immediately before the first read. Let that small read
	// finish while the deliberately large YAML document is still being decoded.
	time.Sleep(50 * time.Millisecond)
	externalEdit := "# external edit must survive\n"
	file, err := os.OpenFile(configPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(externalEdit); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	err = command.Wait()
	if err == nil {
		t.Fatalf("repo add succeeded despite concurrent edit; stdout: %s", stdout.String())
	}
	exitError, ok := err.(*exec.ExitError)
	if !ok || exitError.ExitCode() != 1 {
		t.Fatalf("repo add error = %v, want exit code 1; stderr: %s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "configuration changed") || !strings.Contains(stderr.String(), "retry") {
		t.Fatalf("stderr = %q, want concurrent-edit recovery guidance", stderr.String())
	}
	contents, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(contents), externalEdit) || strings.Contains(string(contents), "service:") {
		t.Fatalf("concurrent edit was overwritten:\n%s", contents)
	}
}

func TestNewCreatesAndListsEmptyTask(t *testing.T) {
	environment := initializedCLIEnvironment(t)

	created := environment.run(t, "new", "billing-rollout")

	if created.code != 0 {
		t.Fatalf("new failed: %s", created.stderr)
	}
	if created.stderr != "" {
		t.Fatalf("new stderr = %q, want empty", created.stderr)
	}
	if !strings.Contains(created.stdout, "billing-rollout") || !strings.Contains(created.stdout, "feat/billing-rollout") {
		t.Fatalf("new output does not identify Task and Task Branch Name:\n%s", created.stdout)
	}

	metadataPath := filepath.Join(environment.dataHome, "devtask", "tasks", "billing-rollout.yaml")
	assertFileMode(t, metadataPath, 0o600)
	contents, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	var metadata persistedTask
	if err := yaml.Unmarshal(contents, &metadata); err != nil {
		t.Fatalf("decode Task metadata: %v\n%s", err, contents)
	}
	if metadata.SchemaVersion != 1 || metadata.Name != "billing-rollout" || metadata.TaskBranchName != "feat/billing-rollout" || metadata.State != "ready" {
		t.Fatalf("Task metadata = %#v", metadata)
	}
	if metadata.CreatedAt.IsZero() || metadata.CreatedAt.Location() != time.UTC {
		t.Fatalf("created_at = %v, want UTC timestamp", metadata.CreatedAt)
	}
	if metadata.Attachments == nil || len(metadata.Attachments) != 0 {
		t.Fatalf("attachments = %#v, want an explicit empty sequence", metadata.Attachments)
	}
	if len(metadata.ContextFiles) != 2 || metadata.ContextFiles[0].Path != "TASK.md" || metadata.ContextFiles[1].Path != "AGENTS.md" {
		t.Fatalf("context_files = %#v, want TASK.md and AGENTS.md ownership data", metadata.ContextFiles)
	}
	for _, contextFile := range metadata.ContextFiles {
		if len(contextFile.SHA256) != 64 {
			t.Fatalf("digest for %s = %q, want SHA-256", contextFile.Path, contextFile.SHA256)
		}
	}

	workspace := filepath.Join(environment.dataHome, "devtask", "workspaces", "billing-rollout")
	assertDirectoryMode(t, workspace, 0o700)
	for _, name := range []string{"TASK.md", "AGENTS.md"} {
		assertFileMode(t, filepath.Join(workspace, name), 0o600)
	}
	taskDocument, err := os.ReadFile(filepath.Join(workspace, "TASK.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(taskDocument), "billing-rollout") {
		t.Fatalf("TASK.md does not identify the Task:\n%s", taskDocument)
	}
	agentsDocument, err := os.ReadFile(filepath.Join(workspace, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"<!-- devtask:generated:start -->", "<!-- devtask:generated:end -->"} {
		if !strings.Contains(string(agentsDocument), marker) {
			t.Fatalf("AGENTS.md does not contain %q:\n%s", marker, agentsDocument)
		}
	}

	human := environment.run(t, "list")
	if human.code != 0 || !strings.Contains(human.stdout, "billing-rollout") || !strings.Contains(human.stdout, "0") || !strings.Contains(human.stdout, metadata.CreatedAt.Format(time.RFC3339)) {
		t.Fatalf("human list did not report the empty Task: code=%d stderr=%q stdout=%q", human.code, human.stderr, human.stdout)
	}
	machine := environment.run(t, "list", "--json")
	if machine.code != 0 {
		t.Fatalf("list --json failed: %s", machine.stderr)
	}
	var tasks []listedTask
	if err := json.Unmarshal([]byte(machine.stdout), &tasks); err != nil {
		t.Fatalf("decode Task list JSON: %v\n%s", err, machine.stdout)
	}
	if len(tasks) != 1 || tasks[0].Name != "billing-rollout" || tasks[0].RepositoryCount != 0 || !tasks[0].CreatedAt.Equal(metadata.CreatedAt) {
		t.Fatalf("Task list = %#v, want billing-rollout with zero repositories", tasks)
	}
}

func TestNewFreezesBranchOverrideAndRejectsCaseInsensitiveDuplicate(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	created := environment.run(t, "new", "Billing", "--branch", "release/billing-v2")
	if created.code != 0 {
		t.Fatalf("new --branch failed: %s", created.stderr)
	}

	metadata := readPersistedTask(t, environment, "Billing")
	if metadata.TaskBranchName != "release/billing-v2" {
		t.Fatalf("Task Branch Name = %q, want override", metadata.TaskBranchName)
	}
	taskDocumentPath := filepath.Join(environment.dataHome, "devtask", "workspaces", "Billing", "TASK.md")
	userContents := []byte("# User-edited Task context\n")
	if err := os.WriteFile(taskDocumentPath, userContents, 0o600); err != nil {
		t.Fatal(err)
	}

	duplicate := environment.run(t, "new", "billing")

	if duplicate.code != 2 {
		t.Fatalf("duplicate exit code = %d, want 2; stderr: %s", duplicate.code, duplicate.stderr)
	}
	if !strings.Contains(duplicate.stderr, "Task \"Billing\" already exists") {
		t.Fatalf("duplicate stderr = %q, want case-insensitive conflict", duplicate.stderr)
	}
	stillEdited, err := os.ReadFile(taskDocumentPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stillEdited, userContents) {
		t.Fatalf("duplicate new rewrote TASK.md:\n%s", stillEdited)
	}
	if listed := listTasks(t, environment); len(listed) != 1 || listed[0].Name != "Billing" {
		t.Fatalf("Tasks after duplicate = %#v, want only Billing", listed)
	}
}

func TestNewRejectsInvalidNamesTemplatesAndBranchNamesWithoutCreatingState(t *testing.T) {
	tests := []struct {
		name          string
		configuration string
		arguments     []string
		message       string
	}{
		{name: "space in Task name", arguments: []string{"new", "bad name"}, message: "invalid Task name"},
		{name: "dot Task name", arguments: []string{"new", "."}, message: "invalid Task name"},
		{name: "leading dash Task name", arguments: []string{"new", "--", "-leading"}, message: "invalid Task name"},
		{
			name:          "template builtin",
			configuration: "schema_version: 1\ndefaults:\n  branch_pattern: 'feat/{{printf \"%s\" .Task}}'\n",
			arguments:     []string{"new", "billing"},
			message:       "only text and {{.Task}}",
		},
		{
			name:          "unknown template value",
			configuration: "schema_version: 1\ndefaults:\n  branch_pattern: 'feat/{{.Repository}}'\n",
			arguments:     []string{"new", "billing"},
			message:       "branch_pattern",
		},
		{
			name:          "invalid rendered Task Branch Name",
			configuration: "schema_version: 1\ndefaults:\n  branch_pattern: 'feat/{{.Task}}..lock'\n",
			arguments:     []string{"new", "billing"},
			message:       "invalid Task Branch Name",
		},
		{name: "invalid branch override", arguments: []string{"new", "billing", "--branch", "bad branch"}, message: "invalid Task Branch Name"},
		{name: "empty branch override", arguments: []string{"new", "billing", "--branch", ""}, message: "invalid Task Branch Name"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment := initializedCLIEnvironment(t)
			if test.configuration != "" {
				configPath := filepath.Join(environment.configHome, "devtask", "config.yaml")
				if err := os.WriteFile(configPath, []byte(test.configuration), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			result := environment.run(t, test.arguments...)

			if result.code != 2 {
				t.Fatalf("exit code = %d, want 2; stderr: %s", result.code, result.stderr)
			}
			if !strings.Contains(result.stderr, test.message) {
				t.Fatalf("stderr = %q, want it to contain %q", result.stderr, test.message)
			}
			if entries, err := os.ReadDir(filepath.Join(environment.dataHome, "devtask", "tasks")); err != nil {
				t.Fatal(err)
			} else {
				for _, entry := range entries {
					if filepath.Ext(entry.Name()) == ".yaml" {
						t.Fatalf("invalid new created Task metadata %q", entry.Name())
					}
				}
			}
			if entries, err := os.ReadDir(filepath.Join(environment.dataHome, "devtask", "workspaces")); err != nil {
				t.Fatal(err)
			} else if len(entries) != 0 {
				t.Fatalf("invalid new created workspace entries: %#v", entries)
			}
		})
	}
}

func TestNewRejectsFilesystemCollisionsWithoutOverwritingThem(t *testing.T) {
	t.Run("Task metadata", func(t *testing.T) {
		environment := initializedCLIEnvironment(t)
		path := filepath.Join(environment.dataHome, "devtask", "tasks", "billing.yaml")
		userContents := []byte("user-owned metadata collision\n")
		if err := os.WriteFile(path, userContents, 0o600); err != nil {
			t.Fatal(err)
		}

		result := environment.run(t, "new", "billing")

		if result.code != 2 || !strings.Contains(result.stderr, "already exists") {
			t.Fatalf("new collision result: code=%d stderr=%q", result.code, result.stderr)
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(contents, userContents) {
			t.Fatalf("metadata collision was overwritten:\n%s", contents)
		}
		if _, err := os.Stat(filepath.Join(environment.dataHome, "devtask", "workspaces", "billing")); !os.IsNotExist(err) {
			t.Fatal("metadata collision created a Task Workspace")
		}
	})

	t.Run("unowned Task Workspace", func(t *testing.T) {
		environment := initializedCLIEnvironment(t)
		workspace := filepath.Join(environment.dataHome, "devtask", "workspaces", "billing")
		if err := os.Mkdir(workspace, 0o700); err != nil {
			t.Fatal(err)
		}
		sentinel := filepath.Join(workspace, "user.txt")
		if err := os.WriteFile(sentinel, []byte("preserve me\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		result := environment.run(t, "new", "billing")

		if result.code != 2 || !strings.Contains(result.stderr, "Task Workspace collision") {
			t.Fatalf("new collision result: code=%d stderr=%q", result.code, result.stderr)
		}
		if contents, err := os.ReadFile(sentinel); err != nil || string(contents) != "preserve me\n" {
			t.Fatalf("workspace collision changed sentinel: contents=%q error=%v", contents, err)
		}
		if _, err := os.Stat(filepath.Join(environment.dataHome, "devtask", "tasks", "billing.yaml")); !os.IsNotExist(err) {
			t.Fatal("workspace collision created Task metadata")
		}
	})
}

func TestNewReportsWorkspaceFilesystemFailureAsOperational(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("devtask v1 supports macOS and Linux")
	}
	environment := initializedCLIEnvironment(t)
	workspaceRoot := filepath.Join(environment.dataHome, "devtask", "workspaces")
	if err := os.Chmod(workspaceRoot, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(workspaceRoot, 0o700) })

	result := environment.run(t, "new", "billing")

	if result.code != 1 {
		t.Fatalf("exit code = %d, want operational exit 1; stderr: %s", result.code, result.stderr)
	}
	if !strings.Contains(result.stderr, "prepare Task Workspace") {
		t.Fatalf("stderr = %q, want Task Workspace filesystem diagnostic", result.stderr)
	}
}

func TestListRejectsTaskMetadataWhoseFilenameDoesNotMatchItsName(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	created := environment.run(t, "new", "Billing")
	if created.code != 0 {
		t.Fatalf("new failed: %s", created.stderr)
	}
	tasksDirectory := filepath.Join(environment.dataHome, "devtask", "tasks")
	if err := os.Rename(filepath.Join(tasksDirectory, "Billing.yaml"), filepath.Join(tasksDirectory, "other.yaml")); err != nil {
		t.Fatal(err)
	}

	result := environment.run(t, "list")

	if result.code != 2 {
		t.Fatalf("exit code = %d, want validation exit 2; stderr: %s", result.code, result.stderr)
	}
	if !strings.Contains(result.stderr, "names Task \"Billing\"; expected \"other\"") {
		t.Fatalf("stderr = %q, want metadata identity diagnostic", result.stderr)
	}
}

func TestNewRollsBackWorkspaceWhenMetadataCannotBeWritten(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("devtask v1 supports macOS and Linux")
	}
	environment := initializedCLIEnvironment(t)
	tasksDirectory := filepath.Join(environment.dataHome, "devtask", "tasks")
	lockPath := filepath.Join(tasksDirectory, ".billing.lock")
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(tasksDirectory, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(tasksDirectory, 0o700) })

	result := environment.run(t, "new", "billing")

	if result.code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr: %s", result.code, result.stderr)
	}
	if !strings.Contains(result.stderr, "write Task metadata") {
		t.Fatalf("stderr = %q, want metadata failure", result.stderr)
	}
	if _, err := os.Stat(filepath.Join(tasksDirectory, "billing.yaml")); !os.IsNotExist(err) {
		t.Fatal("failed new left Task metadata")
	}
	workspaceRoot := filepath.Join(environment.dataHome, "devtask", "workspaces")
	if entries, err := os.ReadDir(workspaceRoot); err != nil {
		t.Fatal(err)
	} else if len(entries) != 0 {
		t.Fatalf("failed new left Task Workspace state: %#v", entries)
	}
}

func TestNewRecoversWorkspacePublishedBeforeMetadata(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	first := environment.run(t, "new", "billing")
	if first.code != 0 {
		t.Fatalf("initial new failed: %s", first.stderr)
	}
	metadataPath := filepath.Join(environment.dataHome, "devtask", "tasks", "billing.yaml")
	if err := os.Remove(metadataPath); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(environment.dataHome, "devtask", "workspaces", "billing")
	taskDocumentPath := filepath.Join(workspace, "TASK.md")
	before, err := os.ReadFile(taskDocumentPath)
	if err != nil {
		t.Fatal(err)
	}

	recovered := environment.run(t, "new", "billing")

	if recovered.code != 0 {
		t.Fatalf("recovering new failed: %s", recovered.stderr)
	}
	after, err := os.ReadFile(taskDocumentPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("recovery rewrote TASK.md:\nbefore: %s\nafter: %s", before, after)
	}
	metadata := readPersistedTask(t, environment, "billing")
	if metadata.State != "ready" || metadata.TaskBranchName != "feat/billing" {
		t.Fatalf("recovered Task metadata = %#v", metadata)
	}
}

func TestNewRejectsGeneratedLookingWorkspaceWithUnmanagedPermissions(t *testing.T) {
	tests := []struct {
		name   string
		change func(string) error
	}{
		{
			name: "workspace directory",
			change: func(workspace string) error {
				return os.Chmod(workspace, 0o755)
			},
		},
		{
			name: "Task Context File",
			change: func(workspace string) error {
				return os.Chmod(filepath.Join(workspace, "TASK.md"), 0o644)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment := initializedCLIEnvironment(t)
			created := environment.run(t, "new", "billing")
			if created.code != 0 {
				t.Fatalf("initial new failed: %s", created.stderr)
			}
			metadataPath := filepath.Join(environment.dataHome, "devtask", "tasks", "billing.yaml")
			if err := os.Remove(metadataPath); err != nil {
				t.Fatal(err)
			}
			workspace := filepath.Join(environment.dataHome, "devtask", "workspaces", "billing")
			if err := test.change(workspace); err != nil {
				t.Fatal(err)
			}

			result := environment.run(t, "new", "billing")

			if result.code != 2 || !strings.Contains(result.stderr, "Task Workspace collision") {
				t.Fatalf("new result: code=%d stderr=%q, want protected collision", result.code, result.stderr)
			}
			if _, err := os.Stat(metadataPath); !os.IsNotExist(err) {
				t.Fatal("unmanaged Workspace permissions produced ready Task metadata")
			}
		})
	}
}

func TestNewRecoversAfterProcessInterruptionAtWorkspacePublication(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("devtask v1 supports macOS and Linux")
	}
	environment := initializedCLIEnvironment(t)
	binary := devtaskBinaryWithTags(t, "devtask_test")
	command := exec.Command(binary, "new", "billing")
	command.Dir = environment.home
	command.Env = append(filteredEnvironment(os.Environ()),
		"HOME="+environment.home,
		"XDG_CONFIG_HOME="+environment.configHome,
		"XDG_DATA_HOME="+environment.dataHome,
		"DEVTASK_TEST_INTERRUPT_AFTER_WORKSPACE=1",
	)
	if err := command.Run(); err == nil {
		t.Fatal("fault-enabled new completed instead of being interrupted")
	}
	metadataPath := filepath.Join(environment.dataHome, "devtask", "tasks", "billing.yaml")
	if _, err := os.Stat(metadataPath); !os.IsNotExist(err) {
		t.Fatal("interrupted new published Task metadata")
	}
	taskDocumentPath := filepath.Join(environment.dataHome, "devtask", "workspaces", "billing", "TASK.md")
	before, err := os.ReadFile(taskDocumentPath)
	if err != nil {
		t.Fatalf("interrupted new did not leave the published Task Workspace: %v", err)
	}

	recovered := environment.run(t, "new", "billing")

	if recovered.code != 0 {
		t.Fatalf("new did not recover after interruption: %s", recovered.stderr)
	}
	after, err := os.ReadFile(taskDocumentPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("recovery rewrote TASK.md:\nbefore: %s\nafter: %s", before, after)
	}
	if metadata := readPersistedTask(t, environment, "billing"); metadata.State != "ready" {
		t.Fatalf("recovered Task state = %q, want ready", metadata.State)
	}
}

func TestNewDoesNotDeleteWorkspaceChangedBeforeCompensation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("devtask v1 supports macOS and Linux")
	}
	environment := initializedCLIEnvironment(t)
	binary := devtaskBinaryWithTags(t, "devtask_test")
	signalPath := filepath.Join(t.TempDir(), "workspace-published")
	command := exec.Command(binary, "new", "billing")
	command.Dir = environment.home
	command.Env = append(filteredEnvironment(os.Environ()),
		"HOME="+environment.home,
		"XDG_CONFIG_HOME="+environment.configHome,
		"XDG_DATA_HOME="+environment.dataHome,
		"DEVTASK_TEST_PAUSE_AFTER_WORKSPACE="+signalPath,
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
			t.Fatal("new did not reach the Workspace publication boundary")
		}
		time.Sleep(time.Millisecond)
	}

	workspace := filepath.Join(environment.dataHome, "devtask", "workspaces", "billing")
	userFile := filepath.Join(workspace, "user.txt")
	if err := os.WriteFile(userFile, []byte("preserve me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tasksDirectory := filepath.Join(environment.dataHome, "devtask", "tasks")
	if err := os.Chmod(tasksDirectory, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(signalPath+".continue", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := command.Wait()
	if chmodError := os.Chmod(tasksDirectory, 0o700); chmodError != nil {
		t.Fatal(chmodError)
	}
	if err == nil {
		t.Fatal("new succeeded despite blocked metadata publication")
	}
	exitError, ok := err.(*exec.ExitError)
	if !ok || exitError.ExitCode() != 1 {
		t.Fatalf("new error = %v, want exit code 1; stderr: %s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "refuse to remove changed Task Workspace") {
		t.Fatalf("stderr = %q, want protected compensation diagnostic", stderr.String())
	}
	if contents, err := os.ReadFile(userFile); err != nil || string(contents) != "preserve me\n" {
		t.Fatalf("compensation removed changed Workspace content: contents=%q error=%v", contents, err)
	}
	if _, err := os.Stat(filepath.Join(tasksDirectory, "billing.yaml")); !os.IsNotExist(err) {
		t.Fatal("failed metadata publication left Task metadata")
	}
}

func TestNewFailsImmediatelyWhenTaskLockIsBusy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("devtask v1 supports macOS and Linux")
	}
	environment := initializedCLIEnvironment(t)
	lockPath := filepath.Join(environment.dataHome, "devtask", "tasks", ".billing.lock")
	held, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	if err := unix.Flock(int(held.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatalf("hold Task lock: %v", err)
	}

	busy := environment.run(t, "new", "billing")

	if busy.code != 1 || !strings.Contains(busy.stderr, "Task \"billing\" is busy") {
		t.Fatalf("busy new result: code=%d stderr=%q", busy.code, busy.stderr)
	}
	if err := unix.Flock(int(held.Fd()), unix.LOCK_UN); err != nil {
		t.Fatalf("release Task lock: %v", err)
	}
	created := environment.run(t, "new", "billing")
	if created.code != 0 {
		t.Fatalf("new after releasing lock failed: %s", created.stderr)
	}
}

func initializedCLIEnvironment(t *testing.T) cliTestEnvironment {
	t.Helper()
	environment := newCLITestEnvironment(t)
	result := environment.run(t, "init")
	if result.code != 0 {
		t.Fatalf("init failed: %s", result.stderr)
	}
	return environment
}

func listRepositories(t *testing.T, environment cliTestEnvironment) []listedRepository {
	t.Helper()
	result := environment.run(t, "repo", "list", "--json")
	if result.code != 0 {
		t.Fatalf("repo list --json failed: %s", result.stderr)
	}
	var repositories []listedRepository
	if err := json.Unmarshal([]byte(result.stdout), &repositories); err != nil {
		t.Fatalf("decode repo list JSON: %v\n%s", err, result.stdout)
	}
	return repositories
}

func listTasks(t *testing.T, environment cliTestEnvironment) []listedTask {
	t.Helper()
	result := environment.run(t, "list", "--json")
	if result.code != 0 {
		t.Fatalf("list --json failed: %s", result.stderr)
	}
	var tasks []listedTask
	if err := json.Unmarshal([]byte(result.stdout), &tasks); err != nil {
		t.Fatalf("decode Task list JSON: %v\n%s", err, result.stdout)
	}
	return tasks
}

func readPersistedTask(t *testing.T, environment cliTestEnvironment, name string) persistedTask {
	t.Helper()
	path := filepath.Join(environment.dataHome, "devtask", "tasks", name+".yaml")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var metadata persistedTask
	if err := yaml.Unmarshal(contents, &metadata); err != nil {
		t.Fatalf("decode Task metadata: %v\n%s", err, contents)
	}
	return metadata
}

func gitRun(t *testing.T, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func assertFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	if info.Mode().Perm() != want {
		t.Fatalf("mode for %q = %04o, want %04o", path, info.Mode().Perm(), want)
	}
}

func assertDirectoryMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	if !info.IsDir() {
		t.Fatalf("%q is not a directory", path)
	}
	if info.Mode().Perm() != want {
		t.Fatalf("mode for %q = %04o, want %04o", path, info.Mode().Perm(), want)
	}
}
