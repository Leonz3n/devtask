package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestRemoveTaskRemovesEveryAttachmentThenWorkspaceAndMetadata(t *testing.T) {
	environment, repositories := createTaskWithAttachments(t, "invoice", "ledger")
	workspace := filepath.Join(environment.dataHome, "devtask", "workspaces", "billing")
	metadataPath := filepath.Join(environment.dataHome, "devtask", "tasks", "billing.yaml")

	result := environment.run(t, "remove", "billing")

	if result.code != 0 {
		t.Fatalf("remove failed: code=%d stderr=%q", result.code, result.stderr)
	}
	if first := strings.Index(result.stdout, "removed Task Worktree for invoice"); first < 0 {
		t.Fatalf("stdout = %q, want invoice result", result.stdout)
	} else if second := strings.Index(result.stdout, "removed Task Worktree for ledger"); second < first {
		t.Fatalf("stdout = %q, want attachment order", result.stdout)
	}
	if !strings.Contains(result.stdout, "removed Task billing") {
		t.Fatalf("stdout = %q, want final Task result", result.stdout)
	}
	for alias, repository := range repositories {
		if _, err := os.Lstat(filepath.Join(repository, ".worktrees", "billing")); !os.IsNotExist(err) {
			t.Fatalf("%s Task Worktree remains: %v", alias, err)
		}
		if branch := gitRun(t, "-C", repository, "branch", "--list", "feat/billing"); branch == "" {
			t.Fatalf("%s Task Branch Name was deleted", alias)
		}
	}
	if _, err := os.Lstat(workspace); !os.IsNotExist(err) {
		t.Fatalf("Task Workspace remains: %v", err)
	}
	if _, err := os.Lstat(metadataPath); !os.IsNotExist(err) {
		t.Fatalf("Task metadata remains: %v", err)
	}
}

func TestRemoveTaskAggregatesEveryPreflightBlockerBeforeDeletingAnything(t *testing.T) {
	environment, repositories := createTaskWithAttachments(t, "invoice", "ledger")
	invoiceWorktree := filepath.Join(repositories["invoice"], ".worktrees", "billing")
	ledgerWorktree := filepath.Join(repositories["ledger"], ".worktrees", "billing")
	if err := os.WriteFile(filepath.Join(invoiceWorktree, "tracked.txt"), []byte("modified\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ledgerWorktree, "untracked.txt"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(environment.dataHome, "devtask", "workspaces", "billing")
	if err := os.WriteFile(filepath.Join(workspace, "TASK.md"), []byte("# edited\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "SPEC.md"), []byte("user-owned\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	agentsPath := filepath.Join(workspace, "AGENTS.md")
	agents, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agentsPath, append([]byte("# Human notes\n\n"), agents...), 0o600); err != nil {
		t.Fatal(err)
	}
	metadata := readPersistedTask(t, environment, "billing")
	metadata.ContextFiles[1].SHA256 = "invalid"
	writePersistedTask(t, environment, metadata)
	if err := os.Remove(filepath.Join(workspace, "ledger")); err != nil {
		t.Fatal(err)
	}
	metadataPath := filepath.Join(environment.dataHome, "devtask", "tasks", "billing.yaml")
	beforeMetadata, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}

	result := environment.run(t, "remove", "billing")

	if result.code != 2 {
		t.Fatalf("remove code=%d stderr=%q, want validation failure", result.code, result.stderr)
	}
	for _, want := range []string{"edited TASK.md", "AGENTS.md", "invalid SHA-256 ownership digest", "new SPEC.md", "Task Workspace link", "invoice", "modified", "ledger", "untracked"} {
		if !strings.Contains(result.stderr, want) {
			t.Fatalf("stderr = %q, want aggregate blocker %q", result.stderr, want)
		}
	}
	for alias, worktree := range map[string]string{"invoice": invoiceWorktree, "ledger": ledgerWorktree} {
		if _, err := os.Lstat(worktree); err != nil {
			t.Fatalf("%s Task Worktree changed during preflight: %v", alias, err)
		}
	}
	afterMetadata, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterMetadata) != string(beforeMetadata) {
		t.Fatalf("Task metadata changed during preflight:\nbefore=%s\nafter=%s", beforeMetadata, afterMetadata)
	}
	if _, err := os.Lstat(workspace); err != nil {
		t.Fatalf("Task Workspace changed during preflight: %v", err)
	}
}

func TestRemoveTaskPersistsCompletedFailedAndUntouchedAttachments(t *testing.T) {
	environment, repositories := createTaskWithAttachments(t, "invoice", "ledger", "audit")
	result := runFaultEnabledCLI(t, environment, map[string]string{
		"DEVTASK_TEST_FAIL_TASK_REMOVE_ALIAS": "ledger",
	}, "remove", "billing")

	if result.code != 1 {
		t.Fatalf("faulted remove code=%d stdout=%q stderr=%q", result.code, result.stdout, result.stderr)
	}
	if !strings.Contains(result.stdout, "removed Task Worktree for invoice") || strings.Contains(result.stdout, "removed Task Worktree for ledger") {
		t.Fatalf("faulted remove stdout=%q", result.stdout)
	}
	for _, want := range []string{"injected Task removal failure for ledger", "completed Repository Attachments: invoice", "failed Repository Attachments: ledger", "untouched Repository Attachments: audit"} {
		if !strings.Contains(result.stderr, want) {
			t.Fatalf("faulted remove stderr=%q, want %q", result.stderr, want)
		}
	}
	if _, err := os.Lstat(filepath.Join(repositories["invoice"], ".worktrees", "billing")); !os.IsNotExist(err) {
		t.Fatalf("completed invoice Task Worktree remains: %v", err)
	}
	for _, alias := range []string{"ledger", "audit"} {
		if _, err := os.Lstat(filepath.Join(repositories[alias], ".worktrees", "billing")); err != nil {
			t.Fatalf("%s Task Worktree was not untouched: %v", alias, err)
		}
	}
	metadata := readPersistedTask(t, environment, "billing")
	if metadata.State != "incomplete" || metadata.Incomplete == nil {
		t.Fatalf("partial Task metadata=%#v", metadata)
	}
	if len(metadata.Attachments) != 2 || metadata.Attachments[0].Alias != "ledger" || metadata.Attachments[0].State != "incomplete" || metadata.Attachments[1].Alias != "audit" || metadata.Attachments[1].State != "ready" {
		t.Fatalf("remaining Repository Attachments=%#v", metadata.Attachments)
	}
	joined := strings.Join(metadata.Incomplete.ResidualObjects, "\n")
	workspace := filepath.Join(environment.dataHome, "devtask", "workspaces", "billing")
	for _, want := range []string{"completed Repository Attachment: invoice", "Task Workspace link is absent: " + filepath.Join(workspace, "invoice"), "generated AGENTS entry is absent: " + filepath.Join(workspace, "AGENTS.md"), "failed Repository Attachment: ledger", "untouched Repository Attachment: audit"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("residuals=%q, want %q", joined, want)
		}
	}
	if _, err := os.Lstat(filepath.Join(workspace, "invoice")); !os.IsNotExist(err) {
		t.Fatalf("completed invoice Task Workspace projection remains: %v", err)
	}
	agents, err := os.ReadFile(filepath.Join(workspace, "AGENTS.md"))
	if err != nil || strings.Contains(string(agents), "`invoice`") {
		t.Fatalf("completed invoice AGENTS projection remains: contents=%q err=%v", agents, err)
	}
	if _, err := os.Lstat(workspace); err != nil {
		t.Fatalf("Task Workspace was removed after partial failure: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(environment.dataHome, "devtask", "tasks", "billing.yaml")); err != nil {
		t.Fatalf("Task metadata was removed after partial failure: %v", err)
	}
}

func TestRemoveTaskRechecksEachAttachmentImmediatelyBeforeDeletion(t *testing.T) {
	environment, repositories := createTaskWithAttachments(t, "invoice", "ledger")
	signalPath := filepath.Join(t.TempDir(), "before-ledger")
	command := exec.Command(devtaskBinaryWithTags(t, "devtask_test"), "remove", "billing", "--force")
	command.Dir = environment.home
	command.Env = append(filteredEnvironment(os.Environ()),
		"HOME="+environment.home,
		"XDG_CONFIG_HOME="+environment.configHome,
		"XDG_DATA_HOME="+environment.dataHome,
		"DEVTASK_TEST_PAUSE_BEFORE_TASK_REMOVE_ALIAS=ledger",
		"DEVTASK_TEST_TASK_REMOVE_SIGNAL="+signalPath,
	)
	var stdout strings.Builder
	var stderr strings.Builder
	command.Stdout = &stdout
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
			t.Fatal("remove did not reach the second Repository Attachment boundary")
		}
		time.Sleep(time.Millisecond)
	}
	ledgerWorktree := filepath.Join(repositories["ledger"], ".worktrees", "billing")
	gitRun(t, "-C", repositories["ledger"], "worktree", "remove", "--force", ledgerWorktree)
	if err := os.Mkdir(ledgerWorktree, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(signalPath+".continue"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := command.Wait()
	exitError, ok := err.(*exec.ExitError)
	if !ok || exitError.ExitCode() != 1 || !strings.Contains(stderr.String(), "Git worktree record") {
		t.Fatalf("identity race: err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if info, err := os.Lstat(ledgerWorktree); err != nil || !info.IsDir() {
		t.Fatalf("replacement Task Worktree path was deleted: info=%v err=%v", info, err)
	}
	metadata := readPersistedTask(t, environment, "billing")
	if metadata.State != "incomplete" || len(metadata.Attachments) != 1 || metadata.Attachments[0].Alias != "ledger" {
		t.Fatalf("identity-race metadata=%#v", metadata)
	}
	if !strings.Contains(strings.Join(metadata.Incomplete.ResidualObjects, "\n"), "generated AGENTS entry remains") {
		t.Fatalf("forced partial removal did not record preserved AGENTS content: %#v", metadata.Incomplete)
	}
}

func TestRemoveTaskForceAuthorizesContentButNotWorkspaceLinkIdentity(t *testing.T) {
	t.Run("protected content", func(t *testing.T) {
		environment, repositories := createTaskWithAttachments(t, "invoice", "ledger")
		if err := os.WriteFile(filepath.Join(repositories["invoice"], ".worktrees", "billing", "valuable.txt"), []byte("authorized\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		exclude, err := os.OpenFile(filepath.Join(repositories["invoice"], ".git", "info", "exclude"), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := exclude.WriteString("\n*.cache\n"); err != nil {
			_ = exclude.Close()
			t.Fatal(err)
		}
		if err := exclude.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repositories["invoice"], ".worktrees", "billing", "local.cache"), []byte("ignored\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		workspace := filepath.Join(environment.dataHome, "devtask", "workspaces", "billing")
		if err := os.WriteFile(filepath.Join(workspace, "TASK.md"), []byte("# user edit\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(workspace, "SPEC.md"), []byte("user file\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("arbitrary user-authored context without generated markers\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		result := environment.run(t, "remove", "billing", "--force")

		if result.code != 0 {
			t.Fatalf("forced remove failed: code=%d stderr=%q", result.code, result.stderr)
		}
		if _, err := os.Lstat(workspace); !os.IsNotExist(err) {
			t.Fatalf("forced remove left Task Workspace: %v", err)
		}
		for alias, repository := range repositories {
			if branch := gitRun(t, "-C", repository, "branch", "--list", "feat/billing"); branch == "" {
				t.Fatalf("--force deleted %s Task Branch Name without --delete-branch", alias)
			}
		}
	})

	t.Run("absolute workspace link identity", func(t *testing.T) {
		environment, repositories := createTaskWithAttachments(t, "invoice")
		worktree := filepath.Join(repositories["invoice"], ".worktrees", "billing")
		link := filepath.Join(environment.dataHome, "devtask", "workspaces", "billing", "invoice")
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join("..", "elsewhere"), link); err != nil {
			t.Fatal(err)
		}

		result := environment.run(t, "remove", "billing", "--force")

		if result.code != 2 || !strings.Contains(result.stderr, "does not target its recorded Task Worktree") {
			t.Fatalf("identity result: code=%d stderr=%q", result.code, result.stderr)
		}
		if _, err := os.Lstat(worktree); err != nil {
			t.Fatalf("--force bypassed Task Workspace link identity: %v", err)
		}
	})

	t.Run("Task Context File ownership identity", func(t *testing.T) {
		environment, repositories := createTaskWithAttachments(t, "invoice")
		metadata := readPersistedTask(t, environment, "billing")
		metadata.ContextFiles[0].SHA256 = "not-a-digest"
		writePersistedTask(t, environment, metadata)
		worktree := filepath.Join(repositories["invoice"], ".worktrees", "billing")

		result := environment.run(t, "remove", "billing", "--force")

		if result.code != 2 || !strings.Contains(result.stderr, "invalid SHA-256 ownership digest") {
			t.Fatalf("ownership result: code=%d stderr=%q", result.code, result.stderr)
		}
		if _, err := os.Lstat(worktree); err != nil {
			t.Fatalf("--force bypassed Task Context File ownership identity: %v", err)
		}
	})
}

func TestRemoveTaskHonorsTaskLockEvenWithForce(t *testing.T) {
	environment, repositories := createTaskWithAttachments(t, "invoice")
	worktree := filepath.Join(repositories["invoice"], ".worktrees", "billing")
	file, err := os.OpenFile(filepath.Join(environment.dataHome, "devtask", "tasks", ".billing.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}

	result := environment.run(t, "remove", "billing", "--force")

	if result.code != 1 || !strings.Contains(result.stderr, "Task \"billing\" is busy") {
		t.Fatalf("busy Task lock: code=%d stderr=%q", result.code, result.stderr)
	}
	if _, err := os.Lstat(worktree); err != nil {
		t.Fatalf("busy Task lock changed Task Worktree: %v", err)
	}
}

func TestRemoveTaskPreflightsGeneratedAgentsProjectionBeforeDeletingAnything(t *testing.T) {
	environment, repositories := createTaskWithAttachments(t, "invoice", "ledger")
	workspace := filepath.Join(environment.dataHome, "devtask", "workspaces", "billing")
	agentsContents := []byte("user content with one malformed marker\n<!-- devtask:generated:start -->\n")
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), agentsContents, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(agentsContents)
	metadata := readPersistedTask(t, environment, "billing")
	metadata.ContextFiles[1].SHA256 = hex.EncodeToString(digest[:])
	writePersistedTask(t, environment, metadata)
	metadataPath := filepath.Join(environment.dataHome, "devtask", "tasks", "billing.yaml")
	before, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}

	result := environment.run(t, "remove", "billing")

	if result.code != 2 || !strings.Contains(result.stderr, "generated marker pair") {
		t.Fatalf("projection preflight: code=%d stderr=%q", result.code, result.stderr)
	}
	for alias, repository := range repositories {
		if _, err := os.Lstat(filepath.Join(repository, ".worktrees", "billing")); err != nil {
			t.Fatalf("projection blocker changed %s Task Worktree: %v", alias, err)
		}
	}
	after, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("projection blocker changed Task metadata:\nbefore=%s\nafter=%s", before, after)
	}
}

func TestRemoveTaskDeletesEveryMergedTaskBranchOnlyWhenRequested(t *testing.T) {
	environment, repositories := createTaskWithAttachments(t, "invoice", "ledger")

	result := environment.run(t, "remove", "billing", "--delete-branch", "--no-fetch")

	if result.code != 0 {
		t.Fatalf("branch removal failed: code=%d stderr=%q", result.code, result.stderr)
	}
	for alias, repository := range repositories {
		if branch := gitRun(t, "-C", repository, "branch", "--list", "feat/billing"); branch != "" {
			t.Fatalf("%s Task Branch Name remains: %q", alias, branch)
		}
		if !strings.Contains(result.stdout, "deleted Task Branch Name feat/billing for "+alias) {
			t.Fatalf("stdout=%q, want %s branch result", result.stdout, alias)
		}
	}
}

func TestRemoveTaskRequiresForceToDeleteAnyUnmergedTaskBranch(t *testing.T) {
	environment, repositories := createTaskWithAttachments(t, "invoice", "ledger")
	invoiceWorktree := filepath.Join(repositories["invoice"], ".worktrees", "billing")
	if err := os.WriteFile(filepath.Join(invoiceWorktree, "task.txt"), []byte("task change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, "-C", invoiceWorktree, "add", "task.txt")
	gitRun(t, "-C", invoiceWorktree, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "task change")

	refused := environment.run(t, "remove", "billing", "--delete-branch", "--no-fetch")
	if refused.code != 2 || !strings.Contains(refused.stderr, "invoice") || !strings.Contains(refused.stderr, "not fully merged") {
		t.Fatalf("unmerged refusal: code=%d stderr=%q", refused.code, refused.stderr)
	}
	for alias, repository := range repositories {
		if _, err := os.Lstat(filepath.Join(repository, ".worktrees", "billing")); err != nil {
			t.Fatalf("preflight changed %s Task Worktree: %v", alias, err)
		}
	}

	forced := environment.run(t, "remove", "billing", "--delete-branch", "--force", "--no-fetch")
	if forced.code != 0 {
		t.Fatalf("forced branch removal failed: code=%d stderr=%q", forced.code, forced.stderr)
	}
	for alias, repository := range repositories {
		if branch := gitRun(t, "-C", repository, "branch", "--list", "feat/billing"); branch != "" {
			t.Fatalf("forced removal left %s Task Branch Name: %q", alias, branch)
		}
	}
}

func TestRemoveTaskReportsEveryBusyRepositoryLockBeforeMutation(t *testing.T) {
	environment, repositories := createTaskWithAttachments(t, "invoice", "ledger")
	for _, repository := range repositories {
		file, err := os.OpenFile(filepath.Join(repository, ".git", "devtask.lock"), os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = file.Close() })
		if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			t.Fatal(err)
		}
	}

	result := environment.run(t, "remove", "billing", "--force")

	if result.code != 1 {
		t.Fatalf("busy lock code=%d stderr=%q", result.code, result.stderr)
	}
	for _, alias := range []string{"invoice", "ledger"} {
		if !strings.Contains(result.stderr, "Registered Repository \""+alias+"\" is busy") {
			t.Fatalf("stderr=%q, want busy %s", result.stderr, alias)
		}
		if _, err := os.Lstat(filepath.Join(repositories[alias], ".worktrees", "billing")); err != nil {
			t.Fatalf("busy preflight changed %s Task Worktree: %v", alias, err)
		}
	}
}

func TestRemoveTaskCheckpointsWorktreeRemovalBeforeBranchAction(t *testing.T) {
	environment, repositories := createTaskWithAttachments(t, "invoice")
	result := runFaultEnabledCLI(t, environment, map[string]string{
		"DEVTASK_TEST_FAIL_TASK_REMOVE_AFTER_WORKTREE_ALIAS": "invoice",
	}, "remove", "billing", "--delete-branch", "--no-fetch")

	if result.code != 1 || !strings.Contains(result.stderr, "injected failure after invoice Task Worktree removal") {
		t.Fatalf("post-worktree fault: code=%d stdout=%q stderr=%q", result.code, result.stdout, result.stderr)
	}
	if _, err := os.Lstat(filepath.Join(repositories["invoice"], ".worktrees", "billing")); !os.IsNotExist(err) {
		t.Fatalf("Task Worktree was not removed: %v", err)
	}
	if branch := gitRun(t, "-C", repositories["invoice"], "branch", "--list", "feat/billing"); branch == "" {
		t.Fatal("fault should happen before Task Branch Name deletion")
	}
	metadata := readPersistedTask(t, environment, "billing")
	if metadata.State != "incomplete" || metadata.Incomplete == nil || len(metadata.Attachments) != 1 || metadata.Attachments[0].State != "incomplete" {
		t.Fatalf("post-worktree checkpoint=%#v", metadata)
	}
	joined := strings.Join(metadata.Attachments[0].ResidualObjects, "\n")
	for _, want := range []string{"Task Worktree path is absent", "Git worktree record is absent", "Task Branch Name remains"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("attachment residuals=%q, want %q", joined, want)
		}
	}
}

func TestRemoveTaskRemovesEmptyTaskAndValidatesArguments(t *testing.T) {
	t.Run("empty Task", func(t *testing.T) {
		environment := initializedCLIEnvironment(t)
		if result := environment.run(t, "new", "billing"); result.code != 0 {
			t.Fatalf("new failed: %s", result.stderr)
		}

		result := environment.run(t, "remove", "billing")

		if result.code != 0 || !strings.Contains(result.stdout, "removed Task billing") {
			t.Fatalf("empty remove: code=%d stdout=%q stderr=%q", result.code, result.stdout, result.stderr)
		}
		if _, err := os.Lstat(filepath.Join(environment.dataHome, "devtask", "workspaces", "billing")); !os.IsNotExist(err) {
			t.Fatalf("empty Task Workspace remains: %v", err)
		}
	})

	t.Run("arguments", func(t *testing.T) {
		environment, repositories := createTaskWithAttachments(t, "invoice")
		worktree := filepath.Join(repositories["invoice"], ".worktrees", "billing")
		for _, test := range []struct {
			arguments []string
			want      string
		}{
			{arguments: []string{"remove"}, want: "accepts 1 arg"},
			{arguments: []string{"remove", "billing", "extra"}, want: "accepts 1 arg"},
			{arguments: []string{"remove", "bad/name"}, want: "invalid Task name"},
			{arguments: []string{"remove", "missing"}, want: "does not exist"},
			{arguments: []string{"remove", "billing", "--fetch", "--no-fetch"}, want: "mutually exclusive"},
		} {
			result := environment.run(t, test.arguments...)
			if result.code != 2 || !strings.Contains(result.stderr, test.want) {
				t.Fatalf("devtask %v: code=%d stderr=%q, want %q", test.arguments, result.code, result.stderr, test.want)
			}
		}
		if _, err := os.Lstat(worktree); err != nil {
			t.Fatalf("invalid request changed Task Worktree: %v", err)
		}
	})
}

func createTaskWithAttachments(t *testing.T, aliases ...string) (cliTestEnvironment, map[string]string) {
	t.Helper()
	environment := initializedCLIEnvironment(t)
	repositories := make(map[string]string, len(aliases))
	for _, alias := range aliases {
		repository := createCommittedRepository(t, alias)
		repositories[alias] = repository
		if result := environment.run(t, "repo", "add", alias, repository); result.code != 0 {
			t.Fatalf("repo add %s failed: %s", alias, result.stderr)
		}
	}
	if result := environment.run(t, "new", "billing"); result.code != 0 {
		t.Fatalf("new failed: %s", result.stderr)
	}
	arguments := append([]string{"add", "billing"}, aliases...)
	arguments = append(arguments, "--no-fetch")
	if result := environment.run(t, arguments...); result.code != 0 {
		t.Fatalf("add failed: %s", result.stderr)
	}
	return environment, repositories
}
