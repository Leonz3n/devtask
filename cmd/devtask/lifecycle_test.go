package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Leonz3n/devtask/internal/config"
)

func TestCompleteLifecycleAcrossMultipleRepositories(t *testing.T) {
	if testing.Short() {
		t.Skip("complete lifecycle test")
	}
	environment := newCLITestEnvironment(t)
	binary := devtaskBinary(t)
	run := func(input string, arguments ...string) commandResult {
		t.Helper()
		return runCompiledCLI(t, binary, environment, input, arguments...)
	}
	requireSuccess := func(result commandResult, operation string) commandResult {
		t.Helper()
		if result.code != 0 {
			t.Fatalf("%s failed: code=%d stdout=%q stderr=%q", operation, result.code, result.stdout, result.stderr)
		}
		return result
	}

	initialized := requireSuccess(run("", "init"), "init")
	if !strings.Contains(initialized.stdout, filepath.Join(environment.configHome, "devtask", "config.yaml")) || initialized.stderr != "" {
		t.Fatalf("init output: stdout=%q stderr=%q", initialized.stdout, initialized.stderr)
	}

	repositories := map[string]string{
		"api":    createCommittedRepository(t, "api service"),
		"web":    createCommittedRepository(t, "web service"),
		"worker": createCommittedRepository(t, "worker service"),
	}
	for _, alias := range []string{"api", "web", "worker"} {
		result := requireSuccess(run("", "repo", "add", alias, repositories[alias]), "register "+alias)
		if !strings.Contains(result.stdout, "registered "+alias+": ") || !strings.Contains(result.stdout, repositories[alias]) {
			t.Fatalf("repo add %s output = %q", alias, result.stdout)
		}
	}

	if err := os.WriteFile(filepath.Join(repositories["api"], ".gitignore"), []byte(".env\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, "-C", repositories["api"], "add", ".gitignore")
	gitRun(t, "-C", repositories["api"], "commit", "-m", "ignore local environment")
	if err := os.WriteFile(filepath.Join(repositories["api"], ".env"), []byte("LOCAL_ONLY=yes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fakeAgent := writeFakeLauncherExecutable(t)
	updateConfiguration(t, environment, func(configuration *config.Config) {
		api := configuration.Repositories["api"]
		api.SharedPaths = []string{".env"}
		configuration.Repositories["api"] = api
		configuration.Groups["delivery"] = []string{"api", "web"}
		configuration.Agents.Pi.Command = fakeAgent
		configuration.Agents.Claude.Command = fakeAgent
		configuration.Agents.Codex.Command = fakeAgent
	})

	empty := requireSuccess(run("", "new", "draft"), "create empty Task")
	if !strings.Contains(empty.stdout, "created Task draft with Task Branch Name feat/draft") {
		t.Fatalf("empty Task output = %q", empty.stdout)
	}
	listedEmpty := requireSuccess(run("", "list", "--json"), "list empty Task")
	var initialTasks []listedTask
	if err := json.Unmarshal([]byte(listedEmpty.stdout), &initialTasks); err != nil {
		t.Fatalf("decode initial Task list: %v\n%s", err, listedEmpty.stdout)
	}
	if len(initialTasks) != 1 || initialTasks[0].Name != "draft" || initialTasks[0].RepositoryCount != 0 || initialTasks[0].CreatedAt.Location() != time.UTC {
		t.Fatalf("initial Task list = %#v", initialTasks)
	}
	requireSuccess(run("", "remove", "draft"), "remove empty Task")

	grouped := requireSuccess(run("", "new", "release", "--group", "delivery", "--no-fetch"), "create grouped Task")
	for _, want := range []string{"created Task release", "attached api to Task release", "attached web to Task release"} {
		if !strings.Contains(grouped.stdout, want) {
			t.Fatalf("grouped Task output = %q, want %q", grouped.stdout, want)
		}
	}
	added := requireSuccess(run("", "add", "release", "worker", "--no-fetch"), "add worker Repository Attachment")
	if !strings.Contains(added.stdout, "attached worker to Task release") {
		t.Fatalf("add output = %q", added.stdout)
	}

	metadata := readPersistedTask(t, environment, "release")
	if len(metadata.Attachments) != 3 {
		t.Fatalf("Repository Attachments = %#v", metadata.Attachments)
	}
	worktrees := make(map[string]string, len(metadata.Attachments))
	for index, attachment := range metadata.Attachments {
		if attachment.Order != index || attachment.State != "ready" || attachment.TaskBranchName != "feat/release" {
			t.Fatalf("Repository Attachment %d = %#v", index, attachment)
		}
		worktrees[attachment.Alias] = attachment.WorktreePath
	}
	sharedPath := filepath.Join(worktrees["api"], ".env")
	if info, err := os.Lstat(sharedPath); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("Shared Local Path %q: info=%v err=%v", sharedPath, info, err)
	}
	if contents, err := os.ReadFile(sharedPath); err != nil || string(contents) != "LOCAL_ONLY=yes\n" {
		t.Fatalf("Shared Local Path contents=%q err=%v", contents, err)
	}

	repositoriesJSON := requireSuccess(run("", "repo", "list", "--json"), "list Registered Repositories")
	var repositoryList []map[string]any
	if err := json.Unmarshal([]byte(repositoriesJSON.stdout), &repositoryList); err != nil {
		t.Fatalf("decode Registered Repository list: %v\n%s", err, repositoriesJSON.stdout)
	}
	if len(repositoryList) != 3 {
		t.Fatalf("Registered Repository list = %#v", repositoryList)
	}
	for _, item := range repositoryList {
		assertJSONKeys(t, item, "alias", "path")
		if path, ok := item["path"].(string); !ok || !filepath.IsAbs(path) {
			t.Fatalf("Registered Repository path = %#v", item["path"])
		}
	}

	tasksJSON := requireSuccess(run("", "list", "--json"), "list Tasks")
	var taskList []map[string]any
	if err := json.Unmarshal([]byte(tasksJSON.stdout), &taskList); err != nil {
		t.Fatalf("decode Task list: %v\n%s", err, tasksJSON.stdout)
	}
	if len(taskList) != 1 {
		t.Fatalf("Task list = %#v", taskList)
	}
	assertJSONKeys(t, taskList[0], "created_at", "name", "repository_count")
	if taskList[0]["name"] != "release" || taskList[0]["repository_count"] != float64(3) {
		t.Fatalf("Task summary = %#v", taskList[0])
	}
	createdAt, ok := taskList[0]["created_at"].(string)
	if !ok {
		t.Fatalf("Task created_at = %#v", taskList[0]["created_at"])
	}
	parsedCreatedAt, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil || parsedCreatedAt.Location() != time.UTC {
		t.Fatalf("Task created_at = %q, err=%v", createdAt, err)
	}

	humanStatus := requireSuccess(run("", "status", "release"), "report human Task status")
	for _, want := range []string{"Task release [ready]", "Task Branch Name: feat/release", "api\tclean", "web\tclean", "worker\tclean"} {
		if !strings.Contains(humanStatus.stdout, want) {
			t.Fatalf("human status = %q, want %q", humanStatus.stdout, want)
		}
	}
	statusJSON := requireSuccess(run("", "status", "release", "--json"), "report JSON Task status")
	var status map[string]any
	if err := json.Unmarshal([]byte(statusJSON.stdout), &status); err != nil {
		t.Fatalf("decode Task status: %v\n%s", err, statusJSON.stdout)
	}
	assertJSONKeys(t, status, "attachments", "created_at", "incomplete", "incomplete_operation", "inspection", "missing", "name", "state", "task_branch_name", "unknown", "workspace_path")
	if workspacePath, ok := status["workspace_path"].(string); !ok || !filepath.IsAbs(workspacePath) {
		t.Fatalf("Task Workspace path = %#v", status["workspace_path"])
	}
	statusAttachments, ok := status["attachments"].([]any)
	if !ok || len(statusAttachments) != 3 {
		t.Fatalf("status Repository Attachments = %#v", status["attachments"])
	}
	for _, rawAttachment := range statusAttachments {
		attachment, ok := rawAttachment.(map[string]any)
		if !ok {
			t.Fatalf("status Repository Attachment = %#v", rawAttachment)
		}
		assertJSONKeys(t, attachment, "alias", "clean", "conflicted", "incomplete", "inspection", "last_error", "main_checkout", "missing", "modified", "residual_objects", "staged", "task_branch_name", "unknown", "untracked", "worktree_path")
		if attachment["clean"] != true || attachment["modified"] != false || attachment["untracked"] != false || attachment["conflicted"] != false {
			t.Fatalf("status flags = %#v", attachment)
		}
		for _, field := range []string{"main_checkout", "worktree_path"} {
			path, ok := attachment[field].(string)
			if !ok || !filepath.IsAbs(path) {
				t.Fatalf("status %s = %#v", field, attachment[field])
			}
		}
	}

	workspace := filepath.Join(environment.dataHome, "devtask", "workspaces", "release")
	for _, agentLauncher := range []string{"pi", "claude", "codex"} {
		launched := requireSuccess(run("lifecycle input\n", agentLauncher, "release", "--", "inspect release"), "launch "+agentLauncher)
		if !strings.Contains(launched.stdout, "stdout=lifecycle input\n") || launched.stderr != "stderr=lifecycle input\n" {
			t.Fatalf("%s streams: stdout=%q stderr=%q", agentLauncher, launched.stdout, launched.stderr)
		}
		if agentLauncher == "pi" && !strings.Contains(launched.stdout, "cwd="+workspace+"\n") {
			t.Fatalf("Pi cwd output = %q", launched.stdout)
		}
		if agentLauncher == "claude" && !strings.Contains(launched.stdout, "cwd="+worktrees["api"]+"\n") {
			t.Fatalf("Claude cwd output = %q", launched.stdout)
		}
		if agentLauncher == "codex" && (!strings.Contains(launched.stdout, "arg[0]=-C\n") || !strings.Contains(launched.stdout, "arg[1]="+worktrees["api"]+"\n")) {
			t.Fatalf("Codex working root output = %q", launched.stdout)
		}
	}
	missingAgent := filepath.Join(t.TempDir(), "missing-agent")
	updateConfiguration(t, environment, func(configuration *config.Config) {
		configuration.Agents.Pi.Command = missingAgent
	})
	startupFailure := run("", "pi", "release", "--", "--api-key=must-not-appear")
	if startupFailure.code != 1 || !strings.Contains(startupFailure.stderr, "resolve Agent Launcher executable") || !strings.Contains(startupFailure.stderr, missingAgent) {
		t.Fatalf("Agent Launcher startup failure: code=%d stdout=%q stderr=%q", startupFailure.code, startupFailure.stdout, startupFailure.stderr)
	}
	if strings.Contains(startupFailure.stderr, "must-not-appear") {
		t.Fatalf("Agent Launcher startup failure leaked a forwarded argument: %q", startupFailure.stderr)
	}

	gitRun(t, "-C", repositories["worker"], "worktree", "remove", "--force", worktrees["worker"])
	drift := requireSuccess(run("", "status", "release"), "report missing Task Worktree")
	if !strings.Contains(drift.stdout, "worker\tmissing") || !strings.Contains(drift.stdout, "devtask remove-repo release worker --forget") {
		t.Fatalf("missing Repository Attachment status = %q", drift.stdout)
	}
	forgotten := requireSuccess(run("", "remove-repo", "release", "worker", "--forget"), "forget externally removed Repository Attachment")
	if !strings.Contains(forgotten.stdout, "forgot Repository Attachment worker from Task release") {
		t.Fatalf("forget output = %q", forgotten.stdout)
	}

	if err := os.WriteFile(filepath.Join(worktrees["web"], "local-change.txt"), []byte("protect me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	blocked := run("", "remove", "release", "--no-fetch")
	if blocked.code != 2 || !strings.Contains(blocked.stderr, "Task Worktree content") || !strings.Contains(blocked.stderr, "Repository Attachment \"web\"") || !strings.Contains(blocked.stderr, "--force") {
		t.Fatalf("protected removal: code=%d stdout=%q stderr=%q", blocked.code, blocked.stdout, blocked.stderr)
	}
	removed := requireSuccess(run("", "remove", "release", "--force", "--no-fetch"), "force-remove Task")
	for _, want := range []string{"removed Task Worktree for api from Task release", "removed Task Worktree for web from Task release", "retained Task Branch Name feat/release for api", "retained Task Branch Name feat/release for web", "removed Task release"} {
		if !strings.Contains(removed.stdout, want) {
			t.Fatalf("remove output = %q, want %q", removed.stdout, want)
		}
	}
	for alias, repository := range repositories {
		if branch := gitRun(t, "-C", repository, "branch", "--list", "feat/release"); branch == "" {
			t.Fatalf("Task Branch Name was not retained for %s", alias)
		}
	}
	for _, path := range []string{
		filepath.Join(environment.dataHome, "devtask", "tasks", "release.yaml"),
		workspace,
		worktrees["api"],
		worktrees["web"],
	} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("cleanup left %q: %v", path, err)
		}
	}
	finalTasks := requireSuccess(run("", "list", "--json"), "list after cleanup")
	if strings.TrimSpace(finalTasks.stdout) != "[]" {
		t.Fatalf("final Task list = %q", finalTasks.stdout)
	}
}

func runCompiledCLI(t *testing.T, binary string, environment cliTestEnvironment, input string, arguments ...string) commandResult {
	t.Helper()
	command := exec.Command(binary, arguments...)
	command.Dir = environment.home
	command.Env = launcherEnvironment(environment, "FAKE_AGENT_EXIT=0")
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
			t.Fatalf("run compiled devtask: %v", err)
		}
		code = exitError.ExitCode()
	}
	return commandResult{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

func assertJSONKeys(t *testing.T, object map[string]any, expected ...string) {
	t.Helper()
	if len(object) != len(expected) {
		t.Fatalf("JSON fields = %#v, want %v", object, expected)
	}
	for _, field := range expected {
		if _, exists := object[field]; !exists {
			t.Fatalf("JSON is missing field %q: %#v", field, object)
		}
	}
}
