package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func devtaskBinary(t *testing.T) string {
	t.Helper()

	binary := filepath.Join(t.TempDir(), "devtask")
	command := exec.Command("go", "build", "-o", binary, ".")
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

func runDevtask(t *testing.T, home, configHome, dataHome string, args ...string) commandResult {
	t.Helper()

	command := exec.Command(devtaskBinary(t), args...)
	command.Dir = home
	command.Env = append(filteredEnvironment(os.Environ()),
		"HOME="+home,
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
	home := t.TempDir()
	configHome := filepath.Join(t.TempDir(), "config")
	dataHome := filepath.Join(t.TempDir(), "data")

	result := runDevtask(t, home, configHome, dataHome, "--help")

	if result.code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", result.code, result.stderr)
	}
	if !strings.Contains(result.stdout, "Usage:") || !strings.Contains(result.stdout, "devtask init") {
		t.Fatalf("help output does not describe the CLI:\n%s", result.stdout)
	}
	for _, path := range []string{filepath.Join(configHome, "devtask"), filepath.Join(dataHome, "devtask")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("help created state path %q", path)
		}
	}
}

func TestInitCreatesVersionedStateInAbsoluteXDGLocations(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("devtask v1 supports macOS and Linux")
	}
	home := t.TempDir()
	configHome := filepath.Join(t.TempDir(), "config")
	dataHome := filepath.Join(t.TempDir(), "data")

	result := runDevtask(t, home, configHome, dataHome, "init")

	if result.code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", result.code, result.stderr)
	}
	if result.stderr != "" {
		t.Fatalf("stderr = %q, want empty", result.stderr)
	}
	configPath := filepath.Join(configHome, "devtask", "config.yaml")
	assertFileMode(t, configPath, 0o600)
	assertDirectoryMode(t, filepath.Join(configHome, "devtask"), 0o700)
	assertDirectoryMode(t, filepath.Join(dataHome, "devtask"), 0o700)
	assertDirectoryMode(t, filepath.Join(dataHome, "devtask", "tasks"), 0o700)
	assertDirectoryMode(t, filepath.Join(dataHome, "devtask", "workspaces"), 0o700)
	contents, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(contents), "schema_version: 1\n") {
		t.Fatalf("config does not start with schema version 1:\n%s", contents)
	}
	if !strings.Contains(result.stdout, configPath) || !strings.Contains(result.stdout, filepath.Join(dataHome, "devtask")) {
		t.Fatalf("init output does not identify persisted state:\n%s", result.stdout)
	}
}

func TestInitIsIdempotentAndPreservesExistingConfiguration(t *testing.T) {
	home := t.TempDir()
	configHome := filepath.Join(t.TempDir(), "config")
	dataHome := filepath.Join(t.TempDir(), "data")
	first := runDevtask(t, home, configHome, dataHome, "init")
	if first.code != 0 {
		t.Fatalf("first init failed: %s", first.stderr)
	}

	configPath := filepath.Join(configHome, "devtask", "config.yaml")
	custom := "schema_version: 1\n\ndefaults:\n  base_branch: trunk\n"
	if err := os.WriteFile(configPath, []byte(custom), 0o600); err != nil {
		t.Fatal(err)
	}
	second := runDevtask(t, home, configHome, dataHome, "init")
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
	home := t.TempDir()

	result := runDevtask(t, home, "relative-config", "relative-data", "init")

	if result.code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", result.code, result.stderr)
	}
	assertFileMode(t, filepath.Join(home, ".config", "devtask", "config.yaml"), 0o600)
	assertDirectoryMode(t, filepath.Join(home, ".local", "share", "devtask"), 0o700)
	for _, path := range []string{filepath.Join(home, "relative-config"), filepath.Join(home, "relative-data")} {
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
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			configHome := filepath.Join(t.TempDir(), "config")
			dataHome := filepath.Join(t.TempDir(), "data")
			configDirectory := filepath.Join(configHome, "devtask")
			if err := os.MkdirAll(configDirectory, 0o700); err != nil {
				t.Fatal(err)
			}
			configPath := filepath.Join(configDirectory, "config.yaml")
			if err := os.WriteFile(configPath, []byte(test.config), 0o600); err != nil {
				t.Fatal(err)
			}

			result := runDevtask(t, home, configHome, dataHome, "init")

			if result.code != 2 {
				t.Fatalf("exit code = %d, want 2; stderr: %s", result.code, result.stderr)
			}
			if !strings.Contains(result.stderr, test.message) {
				t.Fatalf("stderr = %q, want it to contain %q", result.stderr, test.message)
			}
			if _, err := os.Stat(filepath.Join(dataHome, "devtask")); !os.IsNotExist(err) {
				t.Fatalf("invalid configuration created data state")
			}
		})
	}
}

func TestInitFailsClearlyWhenConfigurationLockIsBusy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("devtask v1 supports macOS and Linux")
	}
	home := t.TempDir()
	configHome := filepath.Join(t.TempDir(), "config")
	dataHome := filepath.Join(t.TempDir(), "data")
	configDirectory := filepath.Join(configHome, "devtask")
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

	result := runDevtask(t, home, configHome, dataHome, "init")

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
