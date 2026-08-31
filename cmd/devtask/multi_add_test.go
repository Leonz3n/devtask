package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestAddAttachesSeveralRegisteredRepositoriesInCommandLineOrder(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	repositories := map[string]string{
		"zeta":  createCommittedRepository(t, "zeta"),
		"alpha": createCommittedRepository(t, "alpha"),
	}
	for _, alias := range []string{"zeta", "alpha"} {
		if result := environment.run(t, "repo", "add", alias, repositories[alias]); result.code != 0 {
			t.Fatalf("repo add %s failed: %s", alias, result.stderr)
		}
	}
	if result := environment.run(t, "new", "billing"); result.code != 0 {
		t.Fatalf("new failed: %s", result.stderr)
	}

	result := environment.run(t, "add", "billing", "zeta", "alpha", "--no-fetch")

	if result.code != 0 {
		t.Fatalf("multi-repository add failed: code=%d stderr=%s", result.code, result.stderr)
	}
	if zeta := strings.Index(result.stdout, "attached zeta"); zeta < 0 {
		t.Fatalf("stdout = %q, want zeta result", result.stdout)
	} else if alpha := strings.Index(result.stdout, "attached alpha"); alpha < zeta {
		t.Fatalf("stdout = %q, want command-line result order", result.stdout)
	}
	metadata := readPersistedTask(t, environment, "billing")
	if len(metadata.Attachments) != 2 || metadata.Attachments[0].Alias != "zeta" || metadata.Attachments[0].Order != 0 || metadata.Attachments[1].Alias != "alpha" || metadata.Attachments[1].Order != 1 {
		t.Fatalf("Repository Attachment order = %#v", metadata.Attachments)
	}
	for alias, repository := range repositories {
		worktree := filepath.Join(repository, ".worktrees", "billing")
		if taskBranchName := gitRun(t, "-C", worktree, "branch", "--show-current"); taskBranchName != "feat/billing" {
			t.Fatalf("%s Task Worktree Task Branch Name = %q", alias, taskBranchName)
		}
		link := filepath.Join(environment.dataHome, "devtask", "workspaces", "billing", alias)
		if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s Task Workspace link: info=%v error=%v", alias, info, err)
		}
	}
	agents, err := os.ReadFile(filepath.Join(environment.dataHome, "devtask", "workspaces", "billing", "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if zeta := bytes.Index(agents, []byte("- `zeta`:")); zeta < 0 {
		t.Fatalf("AGENTS.md = %q, want zeta attachment", agents)
	} else if alpha := bytes.Index(agents, []byte("- `alpha`:")); alpha < zeta {
		t.Fatalf("AGENTS.md attachment order = %q, want command-line order", agents)
	}
	metadataPath := filepath.Join(environment.dataHome, "devtask", "tasks", "billing.yaml")
	before, err := os.Lstat(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	repeated := environment.run(t, "add", "billing", "alpha", "zeta", "--no-fetch")
	if repeated.code != 0 || strings.Index(repeated.stdout, "already attached alpha") > strings.Index(repeated.stdout, "already attached zeta") {
		t.Fatalf("idempotent batch result: code=%d stdout=%q stderr=%q", repeated.code, repeated.stdout, repeated.stderr)
	}
	after, err := os.Lstat(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("idempotent batch republished unchanged Task metadata")
	}
}

func TestAddPreflightsEveryRepositoryBeforeMutatingTheFirst(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	first := createCommittedRepository(t, "first")
	second := createCommittedRepository(t, "second")
	for alias, repository := range map[string]string{"first": first, "second": second} {
		if result := environment.run(t, "repo", "add", alias, repository); result.code != 0 {
			t.Fatalf("repo add %s failed: %s", alias, result.stderr)
		}
	}
	if result := environment.run(t, "new", "billing"); result.code != 0 {
		t.Fatalf("new failed: %s", result.stderr)
	}
	secondCollision := filepath.Join(environment.dataHome, "devtask", "workspaces", "billing", "second")
	if err := os.WriteFile(secondCollision, []byte("user owned\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	firstExclude := filepath.Join(first, ".git", "info", "exclude")
	before, err := os.ReadFile(firstExclude)
	if err != nil {
		t.Fatal(err)
	}

	result := environment.run(t, "add", "billing", "first", "second", "--no-fetch")

	if result.code != 2 || !strings.Contains(result.stderr, "Task Workspace collision") {
		t.Fatalf("late preflight result: code=%d stderr=%q", result.code, result.stderr)
	}
	for alias, repository := range map[string]string{"first": first, "second": second} {
		if taskBranchName := gitRun(t, "-C", repository, "branch", "--list", "feat/billing"); taskBranchName != "" {
			t.Fatalf("preflight created Task Branch Name in %s: %q", alias, taskBranchName)
		}
		if _, err := os.Lstat(filepath.Join(repository, ".worktrees", "billing")); !os.IsNotExist(err) {
			t.Fatalf("preflight created Task Worktree in %s: %v", alias, err)
		}
	}
	after, err := os.ReadFile(firstExclude)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("late preflight changed first repository exclude:\nbefore: %s\nafter: %s", before, after)
	}
	if metadata := readPersistedTask(t, environment, "billing"); metadata.State != "ready" || len(metadata.Attachments) != 0 {
		t.Fatalf("late preflight changed Task metadata: %#v", metadata)
	}
}

func TestAddAcquiresUniqueRepositoryLocksInCanonicalPathOrder(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	repositories := map[string]string{"zeta": createCommittedRepository(t, "zeta"), "alpha": createCommittedRepository(t, "alpha")}
	for _, alias := range []string{"zeta", "alpha"} {
		if result := environment.run(t, "repo", "add", alias, repositories[alias]); result.code != 0 {
			t.Fatalf("repo add %s failed: %s", alias, result.stderr)
		}
	}
	if result := environment.run(t, "new", "billing"); result.code != 0 {
		t.Fatalf("new failed: %s", result.stderr)
	}
	recordPath := filepath.Join(t.TempDir(), "locks")
	result := runFaultEnabledCLI(t, environment, map[string]string{"DEVTASK_TEST_RECORD_REPOSITORY_LOCKS": recordPath}, "add", "billing", "zeta", "alpha", "--no-fetch")
	if result.code != 0 {
		t.Fatalf("recorded-lock add failed: %s", result.stderr)
	}
	recorded, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	want := make([]string, 0, len(repositories))
	for _, repository := range repositories {
		canonical, err := filepath.EvalSymlinks(filepath.Join(repository, ".git"))
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, filepath.Join(canonical, "devtask.lock"))
	}
	sort.Strings(want)
	if got := strings.Fields(string(recorded)); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("repository lock order = %#v, want %#v", got, want)
	}
}

func TestAddBusyLaterRepositoryLockAbortsWithoutMutation(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	first := createCommittedRepository(t, "first")
	second := createCommittedRepository(t, "second")
	for alias, repository := range map[string]string{"first": first, "second": second} {
		if result := environment.run(t, "repo", "add", alias, repository); result.code != 0 {
			t.Fatalf("repo add %s failed: %s", alias, result.stderr)
		}
	}
	if result := environment.run(t, "new", "billing"); result.code != 0 {
		t.Fatalf("new failed: %s", result.stderr)
	}
	held, err := os.OpenFile(filepath.Join(second, ".git", "devtask.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := unix.Flock(int(held.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatalf("hold repository lock: %v", err)
	}

	result := environment.run(t, "add", "billing", "first", "second", "--no-fetch")

	if result.code != 1 || !strings.Contains(result.stderr, "is busy") {
		t.Fatalf("busy batch result: code=%d stderr=%q", result.code, result.stderr)
	}
	for alias, repository := range map[string]string{"first": first, "second": second} {
		if taskBranchName := gitRun(t, "-C", repository, "branch", "--list", "feat/billing"); taskBranchName != "" {
			t.Fatalf("busy lock created Task Branch Name in %s: %q", alias, taskBranchName)
		}
	}
	if metadata := readPersistedTask(t, environment, "billing"); len(metadata.Attachments) != 0 {
		t.Fatalf("busy lock persisted attachments: %#v", metadata.Attachments)
	}
}

func TestAddCompensatesInReverseAndPreservesPreexistingBranch(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	existing := createCommittedRepository(t, "existing")
	fresh := createCommittedRepository(t, "fresh")
	gitRun(t, "-C", existing, "branch", "feat/billing", "main")
	for alias, repository := range map[string]string{"existing": existing, "fresh": fresh} {
		if result := environment.run(t, "repo", "add", alias, repository); result.code != 0 {
			t.Fatalf("repo add %s failed: %s", alias, result.stderr)
		}
	}
	if result := environment.run(t, "new", "billing"); result.code != 0 {
		t.Fatalf("new failed: %s", result.stderr)
	}
	workspace := filepath.Join(environment.dataHome, "devtask", "workspaces", "billing")
	originalAgents, err := os.ReadFile(filepath.Join(workspace, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	compensationPath := filepath.Join(t.TempDir(), "compensation")
	result := runFaultEnabledCLI(t, environment, map[string]string{
		"DEVTASK_TEST_FAIL_AFTER_WORKTREE_ALIAS": "fresh",
		"DEVTASK_TEST_RECORD_COMPENSATION":       compensationPath,
	}, "add", "billing", "existing", "fresh", "--no-fetch")
	if result.code != 1 || !strings.Contains(result.stderr, "injected failure after Task Worktree creation for fresh") {
		t.Fatalf("faulted batch result: code=%d stderr=%q", result.code, result.stderr)
	}
	compensation, err := os.ReadFile(compensationPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(compensation) != "fresh\nexisting\n" {
		t.Fatalf("compensation order = %q, want reverse application order", compensation)
	}
	if taskBranchName := gitRun(t, "-C", existing, "branch", "--list", "feat/billing"); !strings.Contains(taskBranchName, "feat/billing") {
		t.Fatalf("compensation removed pre-existing Task Branch Name: %q", taskBranchName)
	}
	if taskBranchName := gitRun(t, "-C", fresh, "branch", "--list", "feat/billing"); taskBranchName != "" {
		t.Fatalf("compensation left invocation-created Task Branch Name: %q", taskBranchName)
	}
	for alias, repository := range map[string]string{"existing": existing, "fresh": fresh} {
		if _, err := os.Lstat(filepath.Join(repository, ".worktrees", "billing")); !os.IsNotExist(err) {
			t.Fatalf("compensation left %s Task Worktree: %v", alias, err)
		}
		if _, err := os.Lstat(filepath.Join(workspace, alias)); !os.IsNotExist(err) {
			t.Fatalf("compensation left %s Task Workspace link: %v", alias, err)
		}
	}
	currentAgents, err := os.ReadFile(filepath.Join(workspace, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(currentAgents, originalAgents) {
		t.Fatalf("compensation did not restore AGENTS.md:\n%s", currentAgents)
	}
	if metadata := readPersistedTask(t, environment, "billing"); metadata.State != "ready" || metadata.Incomplete != nil || len(metadata.Attachments) != 0 {
		t.Fatalf("successful compensation metadata = %#v", metadata)
	}
}

func TestAddCompensatesPublishedExcludesBeforeCreatingWorktrees(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	repositories := map[string]string{"first": createCommittedRepository(t, "first"), "second": createCommittedRepository(t, "second")}
	excludes := make(map[string][]byte, len(repositories))
	for _, alias := range []string{"first", "second"} {
		if result := environment.run(t, "repo", "add", alias, repositories[alias]); result.code != 0 {
			t.Fatalf("repo add %s failed: %s", alias, result.stderr)
		}
		excludePath := filepath.Join(repositories[alias], ".git", "info", "exclude")
		contents, err := os.ReadFile(excludePath)
		if err != nil {
			t.Fatal(err)
		}
		excludes[alias] = contents
	}
	if result := environment.run(t, "new", "billing"); result.code != 0 {
		t.Fatalf("new failed: %s", result.stderr)
	}

	result := runFaultEnabledCLI(t, environment, map[string]string{
		"DEVTASK_TEST_FAIL_AFTER_EXCLUDE_ALIAS": "second",
	}, "add", "billing", "first", "second", "--no-fetch")

	if result.code != 1 || !strings.Contains(result.stderr, "injected failure after local exclude update for second") {
		t.Fatalf("exclude fault result: code=%d stderr=%q", result.code, result.stderr)
	}
	for alias, repository := range repositories {
		excludePath := filepath.Join(repository, ".git", "info", "exclude")
		contents, err := os.ReadFile(excludePath)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(contents, excludes[alias]) {
			t.Fatalf("%s exclude was not restored:\nbefore: %s\nafter: %s", alias, excludes[alias], contents)
		}
		if _, err := os.Lstat(filepath.Join(repository, ".worktrees", "billing")); !os.IsNotExist(err) {
			t.Fatalf("exclude-stage failure created %s Task Worktree: %v", alias, err)
		}
		if taskBranchName := gitRun(t, "-C", repository, "branch", "--list", "feat/billing"); taskBranchName != "" {
			t.Fatalf("exclude-stage failure created %s Task Branch Name: %q", alias, taskBranchName)
		}
	}
	if metadata := readPersistedTask(t, environment, "billing"); metadata.State != "ready" || metadata.Incomplete != nil || len(metadata.Attachments) != 0 {
		t.Fatalf("exclude-stage compensation metadata = %#v", metadata)
	}
}

func TestAddKeepsPublishedBatchWhenMetadataSyncFails(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	repositories := map[string]string{"first": createCommittedRepository(t, "first"), "second": createCommittedRepository(t, "second")}
	for _, alias := range []string{"first", "second"} {
		if result := environment.run(t, "repo", "add", alias, repositories[alias]); result.code != 0 {
			t.Fatalf("repo add %s failed: %s", alias, result.stderr)
		}
	}
	if result := environment.run(t, "new", "billing"); result.code != 0 {
		t.Fatalf("new failed: %s", result.stderr)
	}
	metadataPath := filepath.Join(environment.dataHome, "devtask", "tasks", "billing.yaml")

	result := runFaultEnabledCLI(t, environment, map[string]string{
		"DEVTASK_TEST_FAIL_SYNC_AFTER_PUBLISH": metadataPath,
	}, "add", "billing", "first", "second", "--no-fetch")

	if result.code != 1 || !strings.Contains(result.stderr, "was published") || !strings.Contains(result.stderr, "durably synced") {
		t.Fatalf("metadata publication fault result: code=%d stderr=%q", result.code, result.stderr)
	}
	metadata := readPersistedTask(t, environment, "billing")
	if metadata.State != "ready" || metadata.Incomplete != nil || len(metadata.Attachments) != 2 || metadata.Attachments[0].Alias != "first" || metadata.Attachments[1].Alias != "second" {
		t.Fatalf("published batch metadata = %#v", metadata)
	}
	workspace := filepath.Join(environment.dataHome, "devtask", "workspaces", "billing")
	for alias, repository := range repositories {
		worktree := filepath.Join(repository, ".worktrees", "billing")
		if taskBranchName := gitRun(t, "-C", worktree, "branch", "--show-current"); taskBranchName != "feat/billing" {
			t.Fatalf("published %s Task Branch Name = %q", alias, taskBranchName)
		}
		if resolved, err := filepath.EvalSymlinks(filepath.Join(workspace, alias)); err != nil {
			t.Fatalf("published %s Workspace link: %v", alias, err)
		} else if canonicalWorktree, _ := filepath.EvalSymlinks(worktree); resolved != canonicalWorktree {
			t.Fatalf("published %s Workspace link = %q, want %q", alias, resolved, canonicalWorktree)
		}
	}
}

func TestAddPreservesConcurrentWorktreeAtExpectedPath(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	repository := createCommittedRepository(t, "service")
	if result := environment.run(t, "repo", "add", "service", repository); result.code != 0 {
		t.Fatalf("repo add failed: %s", result.stderr)
	}
	if result := environment.run(t, "new", "billing"); result.code != 0 {
		t.Fatalf("new failed: %s", result.stderr)
	}

	binary := devtaskBinaryWithTags(t, "devtask_test")
	signalPath := filepath.Join(t.TempDir(), "before-move")
	command := exec.Command(binary, "add", "billing", "service", "--no-fetch")
	command.Dir = environment.home
	command.Env = append(filteredEnvironment(os.Environ()),
		"HOME="+environment.home,
		"XDG_CONFIG_HOME="+environment.configHome,
		"XDG_DATA_HOME="+environment.dataHome,
		"DEVTASK_TEST_PAUSE_BEFORE_WORKTREE_MOVE_ALIAS=service",
		"DEVTASK_TEST_PAUSE_BEFORE_WORKTREE_MOVE="+signalPath,
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
			t.Fatal("add did not reach the pre-move boundary")
		}
		time.Sleep(time.Millisecond)
	}
	expectedPath := filepath.Join(repository, ".worktrees", "billing")
	gitRun(t, "-C", repository, "worktree", "add", "--detach", expectedPath, "main")
	if err := os.WriteFile(signalPath+".continue", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := command.Wait()
	if err == nil {
		t.Fatal("add unexpectedly replaced the concurrent Git worktree")
	}
	if exitError, ok := err.(*exec.ExitError); !ok || exitError.ExitCode() != 1 {
		t.Fatalf("add error = %v, want exit 1; stderr: %s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "move Task Worktree") || !strings.Contains(stderr.String(), "incomplete") {
		t.Fatalf("stderr = %q, want expected-path collision diagnostic", stderr.String())
	}
	if taskBranchName := gitRun(t, "-C", expectedPath, "branch", "--show-current"); taskBranchName != "" {
		t.Fatalf("concurrent Git worktree Task Branch Name = %q, want detached", taskBranchName)
	}
	if taskBranchName := gitRun(t, "-C", repository, "branch", "--list", "feat/billing"); taskBranchName != "" {
		t.Fatalf("compensation left invocation-created Task Branch Name: %q", taskBranchName)
	}
	entries, err := os.ReadDir(filepath.Join(repository, ".worktrees"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "billing" {
		t.Fatalf("worktrees directory entries = %#v, want only concurrent billing worktree", entries)
	}
	if metadata := readPersistedTask(t, environment, "billing"); metadata.State != "incomplete" || metadata.Incomplete == nil || len(metadata.Attachments) != 1 || metadata.Attachments[0].Alias != "service" {
		t.Fatalf("concurrent collision metadata = %#v", metadata)
	}
	if _, err := os.Lstat(filepath.Join(environment.dataHome, "devtask", "workspaces", "billing", "service")); !os.IsNotExist(err) {
		t.Fatalf("concurrent collision created Task Workspace link: %v", err)
	}
}

func TestAddRefusesToRemoveReplacedWorktreeDuringCompensation(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	repository := createCommittedRepository(t, "service")
	if result := environment.run(t, "repo", "add", "service", repository); result.code != 0 {
		t.Fatalf("repo add failed: %s", result.stderr)
	}
	if result := environment.run(t, "new", "billing"); result.code != 0 {
		t.Fatalf("new failed: %s", result.stderr)
	}

	binary := devtaskBinaryWithTags(t, "devtask_test")
	signalPath := filepath.Join(t.TempDir(), "before-compensation")
	command := exec.Command(binary, "add", "billing", "service", "--no-fetch")
	command.Dir = environment.home
	command.Env = append(filteredEnvironment(os.Environ()),
		"HOME="+environment.home,
		"XDG_CONFIG_HOME="+environment.configHome,
		"XDG_DATA_HOME="+environment.dataHome,
		"DEVTASK_TEST_FAIL_AFTER_WORKTREE_ALIAS=service",
		"DEVTASK_TEST_PAUSE_BEFORE_COMPENSATION_ALIAS=service",
		"DEVTASK_TEST_PAUSE_BEFORE_COMPENSATION="+signalPath,
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
			t.Fatal("add did not reach compensation boundary")
		}
		time.Sleep(time.Millisecond)
	}
	expectedPath := filepath.Join(repository, ".worktrees", "billing")
	gitRun(t, "-C", repository, "worktree", "remove", "--force", expectedPath)
	gitRun(t, "-C", repository, "worktree", "add", expectedPath, "feat/billing")
	if err := os.WriteFile(signalPath+".continue", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := command.Wait()
	if err == nil {
		t.Fatal("fault-enabled add unexpectedly removed replacement worktree")
	}
	if exitError, ok := err.(*exec.ExitError); !ok || exitError.ExitCode() != 1 {
		t.Fatalf("add error = %v, want exit 1; stderr: %s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "refuse to remove changed Task Worktree path") || !strings.Contains(stderr.String(), "incomplete") {
		t.Fatalf("stderr = %q, want changed-identity refusal", stderr.String())
	}
	if taskBranchName := gitRun(t, "-C", expectedPath, "branch", "--show-current"); taskBranchName != "feat/billing" {
		t.Fatalf("replacement Task Worktree Task Branch Name = %q", taskBranchName)
	}
	metadata := readPersistedTask(t, environment, "billing")
	if metadata.State != "incomplete" || metadata.Incomplete == nil || len(metadata.Attachments) != 1 || metadata.Attachments[0].Alias != "service" {
		t.Fatalf("replacement residual metadata = %#v", metadata)
	}
}

func TestAddPersistsBatchResidualsWhenReverseCompensationIsIncomplete(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	first := createCommittedRepository(t, "first")
	second := createCommittedRepository(t, "second")
	for _, registration := range []struct{ alias, repository string }{{"first", first}, {"second", second}} {
		if result := environment.run(t, "repo", "add", registration.alias, registration.repository); result.code != 0 {
			t.Fatalf("repo add %s failed: %s", registration.alias, result.stderr)
		}
	}
	if result := environment.run(t, "new", "billing"); result.code != 0 {
		t.Fatalf("new failed: %s", result.stderr)
	}

	binary := devtaskBinaryWithTags(t, "devtask_test")
	signalPath := filepath.Join(t.TempDir(), "projection")
	command := exec.Command(binary, "add", "billing", "first", "second", "--no-fetch")
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
			t.Fatal("batch add did not reach the projection boundary")
		}
		time.Sleep(time.Millisecond)
	}
	workspace := filepath.Join(environment.dataHome, "devtask", "workspaces", "billing")
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
		t.Fatal("fault-enabled batch add succeeded")
	}
	if exitError, ok := err.(*exec.ExitError); !ok || exitError.ExitCode() != 1 {
		t.Fatalf("batch add error = %v, want exit 1; stderr: %s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "roll back Repository Attachments") || !strings.Contains(stderr.String(), "incomplete") {
		t.Fatalf("stderr = %q, want incomplete batch rollback diagnostic", stderr.String())
	}
	metadata := readPersistedTask(t, environment, "billing")
	if metadata.State != "incomplete" || metadata.Incomplete == nil || len(metadata.Incomplete.ResidualObjects) == 0 {
		t.Fatalf("incomplete Task metadata = %#v", metadata)
	}
	if len(metadata.Attachments) != 1 || metadata.Attachments[0].Alias != "second" {
		t.Fatalf("incomplete Repository Attachments = %#v", metadata.Attachments)
	}
	if metadata.Attachments[0].State != "incomplete" || metadata.Attachments[0].LastError == "" {
		t.Fatalf("affected Repository Attachment is not incomplete: %#v", metadata.Attachments[0])
	}
	if len(metadata.Attachments[0].ResidualObjects) == 0 || !strings.Contains(strings.Join(metadata.Attachments[0].ResidualObjects, "\n"), secondLink) {
		t.Fatalf("second Repository Attachment residuals = %#v, want changed Workspace entry", metadata.Attachments[0].ResidualObjects)
	}
	if _, err := os.Lstat(filepath.Join(workspace, "first")); !os.IsNotExist(err) {
		t.Fatalf("rollback left first Workspace link: %v", err)
	}
	if contents, err := os.ReadFile(secondLink); err != nil || string(contents) != "user replacement\n" {
		t.Fatalf("rollback changed replacement: contents=%q error=%v", contents, err)
	}
	for alias, repository := range map[string]string{"first": first, "second": second} {
		if _, err := os.Lstat(filepath.Join(repository, ".worktrees", "billing")); !os.IsNotExist(err) {
			t.Fatalf("rollback left %s Task Worktree: %v", alias, err)
		}
		if taskBranchName := gitRun(t, "-C", repository, "branch", "--list", "feat/billing"); taskBranchName != "" {
			t.Fatalf("rollback left %s Task Branch Name: %q", alias, taskBranchName)
		}
	}
	status := environment.run(t, "status", "billing")
	if status.code != 0 || !strings.Contains(status.stdout, "Incomplete operation: add_repository") || !strings.Contains(status.stdout, secondLink) {
		t.Fatalf("status did not expose recovery state: code=%d stdout=%q stderr=%q", status.code, status.stdout, status.stderr)
	}
}

func runFaultEnabledCLI(t *testing.T, environment cliTestEnvironment, extraEnvironment map[string]string, args ...string) commandResult {
	t.Helper()
	command := exec.Command(devtaskBinaryWithTags(t, "devtask_test"), args...)
	command.Dir = environment.home
	command.Env = append(filteredEnvironment(os.Environ()),
		"HOME="+environment.home,
		"XDG_CONFIG_HOME="+environment.configHome,
		"XDG_DATA_HOME="+environment.dataHome,
	)
	for name, value := range extraEnvironment {
		command.Env = append(command.Env, name+"="+value)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	code := 0
	if err != nil {
		exitError, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run fault-enabled devtask: %v", err)
		}
		code = exitError.ExitCode()
	}
	return commandResult{stdout: stdout.String(), stderr: stderr.String(), code: code}
}
