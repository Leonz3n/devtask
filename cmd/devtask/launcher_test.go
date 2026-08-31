package main

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/Leonz3n/devtask/internal/config"
	"gopkg.in/yaml.v3"
)

func TestPiLauncherUsesEmptyTaskWorkspaceAndPreservesProcessContract(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	if result := environment.run(t, "new", "billing"); result.code != 0 {
		t.Fatalf("new failed: %s", result.stderr)
	}
	fake := writeFakeAgent(t)
	configureAgentCommand(t, environment, "pi", fake)

	result := environment.runWithInput(t, "input from terminal\n", "pi", "billing", "--", "--model", "model with spaces", "$HOME")

	workspace := filepath.Join(environment.dataHome, "devtask", "workspaces", "billing")
	if result.code != 23 {
		t.Fatalf("exit code = %d, want child exit code 23; stderr: %s", result.code, result.stderr)
	}
	for _, want := range []string{
		"cwd=" + workspace,
		"arg[0]=--model",
		"arg[1]=model with spaces",
		"arg[2]=$HOME",
		"stdout=input from terminal",
	} {
		if !strings.Contains(result.stdout, want+"\n") {
			t.Fatalf("stdout = %q, want line %q", result.stdout, want)
		}
	}
	if result.stderr != "stderr=input from terminal\n" {
		t.Fatalf("stderr = %q, want child stderr only", result.stderr)
	}
}

func TestClaudeLauncherUsesFirstTaskWorktreeAndAddsRemainingTaskDirectories(t *testing.T) {
	environment, worktrees := taskWithTwoAttachments(t)
	fake := writeFakeAgent(t)
	configureAgentCommand(t, environment, "claude", fake)

	result := environment.runWithInput(t, "hello\n", "claude", "billing", "--", "--add-dir=/user/extra", "review this")

	workspace := filepath.Join(environment.dataHome, "devtask", "workspaces", "billing")
	if result.code != 23 {
		t.Fatalf("exit code = %d, want 23; stderr: %s", result.code, result.stderr)
	}
	wantLines := []string{
		"cwd=" + worktrees[0],
		"arg[0]=--add-dir=" + worktrees[1],
		"arg[1]=--add-dir=" + workspace,
		"arg[2]=--add-dir=/user/extra",
		"arg[3]=review this",
	}
	assertOutputLines(t, result.stdout, wantLines)
}

func TestCodexLauncherUsesRecordedFirstTaskWorktreeAndAddsRemainingTaskDirectories(t *testing.T) {
	environment, worktrees := taskWithTwoAttachments(t)
	fake := writeFakeAgent(t)
	configureAgentCommand(t, environment, "codex", fake)

	result := environment.runWithInput(t, "hello\n", "codex", "billing", "--", "--add-dir", "/user/extra", "review this")

	workspace := filepath.Join(environment.dataHome, "devtask", "workspaces", "billing")
	if result.code != 23 {
		t.Fatalf("exit code = %d, want 23; stderr: %s", result.code, result.stderr)
	}
	wantLines := []string{
		"cwd=" + workspace,
		"arg[0]=-C",
		"arg[1]=" + worktrees[0],
		"arg[2]=--add-dir",
		"arg[3]=" + worktrees[1],
		"arg[4]=--add-dir",
		"arg[5]=" + workspace,
		"arg[6]=--add-dir",
		"arg[7]=/user/extra",
		"arg[8]=review this",
	}
	assertOutputLines(t, result.stdout, wantLines)
}

func TestClaudeAndCodexLaunchEmptyTaskFromTaskWorkspaceWithoutInjectedDirectories(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			environment := initializedCLIEnvironment(t)
			if result := environment.run(t, "new", "billing"); result.code != 0 {
				t.Fatalf("new failed: %s", result.stderr)
			}
			fake := writeFakeAgent(t)
			configureAgentCommand(t, environment, agent, fake)

			result := environment.runWithInput(t, "hello\n", agent, "billing", "--", "prompt")

			workspace := filepath.Join(environment.dataHome, "devtask", "workspaces", "billing")
			if result.code != 23 {
				t.Fatalf("exit code = %d, want 23; stderr: %s", result.code, result.stderr)
			}
			assertOutputLines(t, result.stdout, []string{"cwd=" + workspace, "arg[0]=prompt"})
			if strings.Contains(result.stdout, "arg[1]=") {
				t.Fatalf("empty Task received an injected directory argument:\n%s", result.stdout)
			}
		})
	}
}

func TestAgentLaunchersRejectArgumentsThatTakeOverManagedTaskWorktrees(t *testing.T) {
	tests := []struct {
		name  string
		agent string
		args  []string
	}{
		{name: "Claude worktree flag", agent: "claude", args: []string{"--worktree", "other"}},
		{name: "Claude worktree equals flag", agent: "claude", args: []string{"--worktree=other"}},
		{name: "Claude short worktree flag", agent: "claude", args: []string{"-w", "other"}},
		{name: "Claude compact short worktree flag", agent: "claude", args: []string{"-wother"}},
		{name: "Codex working root flag", agent: "codex", args: []string{"-C", "/tmp/other"}},
		{name: "Codex compact working root flag", agent: "codex", args: []string{"-C/tmp/other"}},
		{name: "Codex long working root flag", agent: "codex", args: []string{"--cd=/tmp/other"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment := initializedCLIEnvironment(t)
			if result := environment.run(t, "new", "billing"); result.code != 0 {
				t.Fatalf("new failed: %s", result.stderr)
			}
			configureAgentCommand(t, environment, test.agent, writeFakeAgent(t))

			arguments := append([]string{test.agent, "billing", "--"}, test.args...)
			result := environment.run(t, arguments...)

			if result.code != 2 {
				t.Fatalf("exit code = %d, want validation exit code 2; stdout: %s; stderr: %s", result.code, result.stdout, result.stderr)
			}
			if result.stdout != "" || !strings.Contains(result.stderr, "conflicts with") {
				t.Fatalf("launcher did not reject conflicting arguments before execution: stdout=%q stderr=%q", result.stdout, result.stderr)
			}
		})
	}
}

func TestAgentLauncherHoldsSharedTaskAndRepositoryLocksForChildLifetime(t *testing.T) {
	environment, _ := taskWithTwoAttachments(t)
	configureAgentCommand(t, environment, "pi", writeFakeAgent(t))
	binary := devtaskBinary(t)

	command := exec.Command(binary, "pi", "billing")
	command.Dir = environment.home
	command.Env = launcherEnvironment(environment, "FAKE_AGENT_EXIT=0")
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(stdout)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("wait for fake agent readiness: %v; stderr: %s", err, stderr.String())
		}
		if line == "ready\n" {
			break
		}
	}

	sameTask := environment.run(t, "add", "billing", "zebra-service", "--no-fetch")
	if sameTask.code != 1 || !strings.Contains(sameTask.stderr, "Task \"billing\" is busy") {
		t.Fatalf("same-Task mutation was not blocked by shared Task lock: code=%d stderr=%q", sameTask.code, sameTask.stderr)
	}
	if result := environment.run(t, "new", "other-task"); result.code != 0 {
		t.Fatalf("new other Task failed: %s", result.stderr)
	}
	sharedRepository := environment.run(t, "add", "other-task", "zebra-service", "--no-fetch")
	if sharedRepository.code != 1 || !strings.Contains(sharedRepository.stderr, "Registered Repository \"zebra-service\" is busy") {
		t.Fatalf("repository mutation was not blocked by shared repository lock: code=%d stderr=%q", sharedRepository.code, sharedRepository.stderr)
	}

	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("launcher exit: %v; stderr: %s", err, stderr.String())
	}
	afterExit := environment.run(t, "add", "other-task", "zebra-service", "--no-fetch")
	if afterExit.code != 0 {
		t.Fatalf("repository lock remained held after child exit: %s", afterExit.stderr)
	}
}

func TestAgentLauncherForwardsTerminationSignalToChild(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	if result := environment.run(t, "new", "billing"); result.code != 0 {
		t.Fatalf("new failed: %s", result.stderr)
	}
	configureAgentCommand(t, environment, "pi", writeSignalAgent(t))
	command := exec.Command(devtaskBinary(t), "pi", "billing")
	command.Dir = environment.home
	command.Env = launcherEnvironment(environment)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "ready\n" {
		t.Fatalf("wait for fake agent readiness: line=%q err=%v stderr=%s", line, err, stderr.String())
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	err = command.Wait()
	var exitError *exec.ExitError
	if !asExitError(err, &exitError) || exitError.ExitCode() != 47 {
		t.Fatalf("launcher exit = %v, want child exit code 47; stderr: %s", err, stderr.String())
	}
	if stderr.String() != "signal=TERM\n" {
		t.Fatalf("stderr = %q, want forwarded signal observation", stderr.String())
	}
}

func TestAgentLauncherResolvesConfiguredExecutableNameFromPath(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	if result := environment.run(t, "new", "billing"); result.code != 0 {
		t.Fatalf("new failed: %s", result.stderr)
	}
	fake := writeFakeAgent(t)
	named := filepath.Join(filepath.Dir(fake), "custom-pi")
	if err := os.Rename(fake, named); err != nil {
		t.Fatal(err)
	}
	configureAgentCommand(t, environment, "pi", filepath.Base(named))

	result := environment.runWithInputAndEnvironment(t, "hello\n", []string{"PATH=" + filepath.Dir(named) + string(os.PathListSeparator) + os.Getenv("PATH")}, "pi", "billing")

	if result.code != 23 || !strings.Contains(result.stdout, "ready\n") {
		t.Fatalf("named executable was not resolved from PATH: code=%d stdout=%q stderr=%q", result.code, result.stdout, result.stderr)
	}
}

func TestAgentLauncherDoesNotInterpretConfiguredCommandAsShell(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	if result := environment.run(t, "new", "billing"); result.code != 0 {
		t.Fatalf("new failed: %s", result.stderr)
	}
	marker := filepath.Join(t.TempDir(), "shell-was-used")
	configureAgentCommand(t, environment, "pi", writeFakeAgent(t)+"; touch "+marker)

	result := environment.run(t, "pi", "billing")

	if result.code != 1 || !strings.Contains(result.stderr, "resolve agent executable") {
		t.Fatalf("shell-like command did not fail as one executable name: code=%d stderr=%q", result.code, result.stderr)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("configured command was interpreted by a shell; marker stat: %v", err)
	}
}

func taskWithTwoAttachments(t *testing.T) (cliTestEnvironment, []string) {
	t.Helper()
	environment := initializedCLIEnvironment(t)
	repositoriesRoot := t.TempDir()
	aliases := []string{"zebra-service", "alpha-service"}
	for _, alias := range aliases {
		repository := filepath.Join(repositoriesRoot, alias)
		gitRun(t, "init", "-b", "main", repository)
		if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte(alias+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitRun(t, "-C", repository, "add", "README.md")
		gitRun(t, "-C", repository, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "initial")
		if result := environment.run(t, "repo", "add", alias, repository); result.code != 0 {
			t.Fatalf("repo add %s failed: %s", alias, result.stderr)
		}
	}
	if result := environment.run(t, "new", "billing"); result.code != 0 {
		t.Fatalf("new failed: %s", result.stderr)
	}
	if result := environment.run(t, "add", "billing", aliases[0], aliases[1], "--no-fetch"); result.code != 0 {
		t.Fatalf("add failed: %s", result.stderr)
	}
	metadata := readPersistedTask(t, environment, "billing")
	worktrees := make([]string, 0, len(metadata.Attachments))
	for _, attachment := range metadata.Attachments {
		worktrees = append(worktrees, attachment.WorktreePath)
	}
	return environment, worktrees
}

func assertOutputLines(t *testing.T, output string, lines []string) {
	t.Helper()
	for _, line := range lines {
		if !strings.Contains(output, line+"\n") {
			t.Fatalf("output = %q, want line %q", output, line)
		}
	}
}

func (environment cliTestEnvironment) runWithInput(t *testing.T, input string, args ...string) commandResult {
	return environment.runWithInputAndEnvironment(t, input, nil, args...)
}

func (environment cliTestEnvironment) runWithInputAndEnvironment(t *testing.T, input string, extraEnvironment []string, args ...string) commandResult {
	t.Helper()
	command := exec.Command(devtaskBinary(t), args...)
	command.Dir = environment.home
	command.Env = launcherEnvironment(environment, append([]string{"FAKE_AGENT_EXIT=23"}, extraEnvironment...)...)
	command.Stdin = strings.NewReader(input)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	code := 0
	if err != nil {
		var exitError *exec.ExitError
		if !asExitError(err, &exitError) {
			t.Fatalf("run devtask: %v", err)
		}
		code = exitError.ExitCode()
	}
	return commandResult{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

func writeFakeAgent(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake agent")
	script := `#!/bin/sh
printf 'cwd=%s\n' "$PWD"
index=0
for argument do
  printf 'arg[%d]=%s\n' "$index" "$argument"
  index=$((index + 1))
done
printf 'ready\n'
IFS= read -r input
printf 'stdout=%s\n' "$input"
printf 'stderr=%s\n' "$input" >&2
exit "${FAKE_AGENT_EXIT:-0}"
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeSignalAgent(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "signal-agent")
	script := `#!/bin/sh
trap 'printf "signal=TERM\n" >&2; exit 47' TERM
printf 'ready\n'
while :; do
  sleep 1
done
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func launcherEnvironment(environment cliTestEnvironment, extra ...string) []string {
	values := append(filteredEnvironment(os.Environ()),
		"HOME="+environment.home,
		"XDG_CONFIG_HOME="+environment.configHome,
		"XDG_DATA_HOME="+environment.dataHome,
	)
	return append(values, extra...)
}

func configureAgentCommand(t *testing.T, environment cliTestEnvironment, agent, command string) {
	t.Helper()
	path := filepath.Join(environment.configHome, "devtask", "config.yaml")
	configuration, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	switch agent {
	case "pi":
		configuration.Agents.Pi.Command = command
	case "claude":
		configuration.Agents.Claude.Command = command
	case "codex":
		configuration.Agents.Codex.Command = command
	default:
		t.Fatalf("unknown agent %q", agent)
	}
	updated, err := yaml.Marshal(configuration)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, updated, 0o600); err != nil {
		t.Fatal(err)
	}
}
