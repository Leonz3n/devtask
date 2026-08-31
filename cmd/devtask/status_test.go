package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

type taskStatusJSON struct {
	Name           string                 `json:"name"`
	TaskBranchName string                 `json:"task_branch_name"`
	CreatedAt      time.Time              `json:"created_at"`
	State          string                 `json:"state"`
	WorkspacePath  string                 `json:"workspace_path"`
	Missing        bool                   `json:"missing"`
	Unknown        bool                   `json:"unknown"`
	IncompleteFlag bool                   `json:"incomplete"`
	Inspection     *statusInspectionJSON  `json:"inspection"`
	Incomplete     *statusOperationJSON   `json:"incomplete_operation"`
	Attachments    []attachmentStatusJSON `json:"attachments"`
}

type attachmentStatusJSON struct {
	Alias          string                `json:"alias"`
	MainCheckout   string                `json:"main_checkout"`
	WorktreePath   string                `json:"worktree_path"`
	TaskBranchName string                `json:"task_branch_name"`
	Clean          bool                  `json:"clean"`
	Modified       bool                  `json:"modified"`
	Staged         bool                  `json:"staged"`
	Untracked      bool                  `json:"untracked"`
	Conflicted     bool                  `json:"conflicted"`
	Missing        bool                  `json:"missing"`
	Unknown        bool                  `json:"unknown"`
	Incomplete     bool                  `json:"incomplete"`
	Inspection     *statusInspectionJSON `json:"inspection"`
	LastError      string                `json:"last_error"`
	Residuals      []string              `json:"residual_objects"`
}

type statusInspectionJSON struct {
	Operation string   `json:"operation"`
	Message   string   `json:"message"`
	Recovery  []string `json:"recovery"`
}

type statusOperationJSON struct {
	Operation       string   `json:"operation"`
	LastError       string   `json:"last_error"`
	ResidualObjects []string `json:"residual_objects"`
	Recovery        []string `json:"recovery"`
}

func TestStatusReportsCleanRepositoryAttachment(t *testing.T) {
	environment, repository, worktree := createAttachedTask(t, "invoice")

	human := environment.run(t, "status", "billing")
	if human.code != 0 {
		t.Fatalf("status failed: code=%d stderr=%s", human.code, human.stderr)
	}
	for _, want := range []string{"Task billing", "ready", "invoice", "clean", worktree} {
		if !strings.Contains(human.stdout, want) {
			t.Fatalf("human status = %q, want it to contain %q", human.stdout, want)
		}
	}

	machine := environment.run(t, "status", "billing", "--json")
	if machine.code != 0 {
		t.Fatalf("status --json failed: code=%d stderr=%s", machine.code, machine.stderr)
	}
	var report taskStatusJSON
	if err := json.Unmarshal([]byte(machine.stdout), &report); err != nil {
		t.Fatalf("decode status JSON: %v\n%s", err, machine.stdout)
	}
	if report.Name != "billing" || report.TaskBranchName != "feat/billing" || report.State != "ready" {
		t.Fatalf("Task status = %#v", report)
	}
	if report.CreatedAt.Location() != time.UTC || report.WorkspacePath != filepath.Join(environment.dataHome, "devtask", "workspaces", "billing") || !filepath.IsAbs(report.WorkspacePath) {
		t.Fatalf("Task status paths/time = %#v", report)
	}
	if report.Incomplete != nil || len(report.Attachments) != 1 {
		t.Fatalf("Task status = %#v, want one attachment and no incomplete operation", report)
	}
	attachment := report.Attachments[0]
	canonicalRepository, err := filepath.EvalSymlinks(repository)
	if err != nil {
		t.Fatal(err)
	}
	if attachment.Alias != "invoice" || attachment.MainCheckout != canonicalRepository || attachment.WorktreePath != worktree || attachment.TaskBranchName != "feat/billing" {
		t.Fatalf("attachment identity = %#v", attachment)
	}
	if !attachment.Clean || attachment.Modified || attachment.Staged || attachment.Untracked || attachment.Conflicted || attachment.Missing || attachment.Unknown || attachment.Incomplete || attachment.Inspection != nil || attachment.LastError != "" || attachment.Residuals == nil {
		t.Fatalf("clean attachment status = %#v", attachment)
	}
}

func TestStatusCombinesModifiedStagedUntrackedAndRenameWithUnusualNames(t *testing.T) {
	environment, _, worktree := createAttachedTask(t, "invoice")
	if err := os.WriteFile(filepath.Join(worktree, "rename-source.txt"), []byte("rename me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, "-C", worktree, "add", "rename-source.txt")
	gitRun(t, "-C", worktree, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "add rename source")
	if err := os.WriteFile(filepath.Join(worktree, "tracked.txt"), []byte("modified\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "staged.txt"), []byte("staged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, "-C", worktree, "add", "staged.txt")
	gitRun(t, "-C", worktree, "mv", "rename-source.txt", "renamed\nfile.txt")
	if err := os.WriteFile(filepath.Join(worktree, "untracked\n\tfile.txt"), []byte("untracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	report := readTaskStatus(t, environment)
	attachment := report.Attachments[0]
	if attachment.Clean || !attachment.Modified || !attachment.Staged || !attachment.Untracked || attachment.Conflicted || attachment.Missing || attachment.Unknown || attachment.Incomplete {
		t.Fatalf("combined attachment status = %#v", attachment)
	}
	human := environment.run(t, "status", "billing")
	if human.code != 0 || !strings.Contains(human.stdout, "modified, staged, untracked") {
		t.Fatalf("human combined status: code=%d stdout=%q stderr=%q", human.code, human.stdout, human.stderr)
	}
}

func TestStatusReportsIndividualGitStates(t *testing.T) {
	tests := []struct {
		name   string
		change func(*testing.T, string)
		check  func(attachmentStatusJSON) bool
	}{
		{
			name: "modified",
			change: func(t *testing.T, worktree string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(worktree, "tracked.txt"), []byte("modified\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			check: func(status attachmentStatusJSON) bool {
				return status.Modified && !status.Staged && !status.Untracked && !status.Conflicted
			},
		},
		{
			name: "staged",
			change: func(t *testing.T, worktree string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(worktree, "staged.txt"), []byte("staged\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				gitRun(t, "-C", worktree, "add", "staged.txt")
			},
			check: func(status attachmentStatusJSON) bool {
				return !status.Modified && status.Staged && !status.Untracked && !status.Conflicted
			},
		},
		{
			name: "untracked",
			change: func(t *testing.T, worktree string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(worktree, "untracked.txt"), []byte("untracked\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			check: func(status attachmentStatusJSON) bool {
				return !status.Modified && !status.Staged && status.Untracked && !status.Conflicted
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment, _, worktree := createAttachedTask(t, "invoice")
			test.change(t, worktree)
			status := readTaskStatus(t, environment).Attachments[0]
			if status.Clean || !test.check(status) {
				t.Fatalf("%s attachment status = %#v", test.name, status)
			}
		})
	}
}

func TestStatusCombinesConflictedModifiedAndUntracked(t *testing.T) {
	environment, repository, worktree := createAttachedTask(t, "invoice")
	if err := os.WriteFile(filepath.Join(worktree, "other.txt"), []byte("other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, "-C", worktree, "add", "other.txt")
	gitRun(t, "-C", worktree, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "task setup")
	if err := os.WriteFile(filepath.Join(worktree, "tracked.txt"), []byte("task version\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, "-C", worktree, "add", "tracked.txt")
	gitRun(t, "-C", worktree, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "task change")
	if err := os.WriteFile(filepath.Join(repository, "tracked.txt"), []byte("main version\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, "-C", repository, "add", "tracked.txt")
	gitRun(t, "-C", repository, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "main change")
	merge := exec.Command("git", "-C", worktree, "-c", "user.name=Test", "-c", "user.email=test@example.com", "merge", "main")
	if output, err := merge.CombinedOutput(); err == nil {
		t.Fatalf("merge unexpectedly succeeded: %s", output)
	}
	if err := os.WriteFile(filepath.Join(worktree, "other.txt"), []byte("modified after conflict\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "untracked.txt"), []byte("untracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	status := readTaskStatus(t, environment).Attachments[0]
	if status.Clean || !status.Modified || !status.Untracked || !status.Conflicted || status.Missing || status.Unknown {
		t.Fatalf("conflicted combined status = %#v", status)
	}
	human := environment.run(t, "status", "billing")
	if human.code != 0 || !strings.Contains(human.stdout, "modified, untracked, conflicted") {
		t.Fatalf("human conflict status: code=%d stdout=%q stderr=%q", human.code, human.stdout, human.stderr)
	}
}

func TestStatusReportsMissingTaskWorktreePathAndRecord(t *testing.T) {
	t.Run("path", func(t *testing.T) {
		environment, _, worktree := createAttachedTask(t, "invoice")
		if err := os.Rename(worktree, worktree+"-moved"); err != nil {
			t.Fatal(err)
		}

		status := readTaskStatus(t, environment).Attachments[0]
		assertMissingStatus(t, status, "Task Worktree path")
	})

	t.Run("Git worktree record", func(t *testing.T) {
		environment, repository, worktree := createAttachedTask(t, "invoice")
		gitRun(t, "-C", repository, "worktree", "remove", "--force", worktree)
		if err := os.MkdirAll(worktree, 0o700); err != nil {
			t.Fatal(err)
		}

		status := readTaskStatus(t, environment).Attachments[0]
		assertMissingStatus(t, status, "Git worktree record")
	})
}

func TestStatusReportsUnknownGitInspectionFailure(t *testing.T) {
	environment, repository, _ := createAttachedTask(t, "invoice")
	if err := os.Rename(filepath.Join(repository, ".git"), filepath.Join(repository, ".git-away")); err != nil {
		t.Fatal(err)
	}

	result := environment.run(t, "status", "billing", "--json")
	if result.code != 0 {
		t.Fatalf("status should report inspection failure: code=%d stderr=%s", result.code, result.stderr)
	}
	var report taskStatusJSON
	if err := json.Unmarshal([]byte(result.stdout), &report); err != nil {
		t.Fatal(err)
	}
	status := report.Attachments[0]
	if status.Clean || status.Missing || !status.Unknown || status.Inspection == nil || status.Inspection.Operation == "" || status.Inspection.Message == "" || len(status.Inspection.Recovery) == 0 {
		t.Fatalf("unknown attachment status = %#v", status)
	}
	human := environment.run(t, "status", "billing")
	if human.code != 0 || !strings.Contains(human.stdout, "unknown") || !strings.Contains(human.stdout, status.Inspection.Operation) || !strings.Contains(human.stdout, status.Inspection.Recovery[0]) {
		t.Fatalf("human unknown status: code=%d stdout=%q stderr=%q", human.code, human.stdout, human.stderr)
	}
}

func TestStatusDisplaysPersistedIncompleteOperationAndAttachment(t *testing.T) {
	environment, _, _ := createAttachedTask(t, "invoice")
	metadata := readPersistedTask(t, environment, "billing")
	metadata.State = "incomplete"
	metadata.Incomplete = &struct {
		Operation       string   `yaml:"operation"`
		LastError       string   `yaml:"last_error"`
		ResidualObjects []string `yaml:"residual_objects"`
		Recovery        []string `yaml:"recovery"`
	}{
		Operation:       "add_repository",
		LastError:       "projection rollback failed",
		ResidualObjects: []string{"Task Workspace entry remains"},
		Recovery:        []string{"restore the Task Workspace entry"},
	}
	metadata.Attachments[0].State = "incomplete"
	metadata.Attachments[0].LastError = "could not restore projection"
	metadata.Attachments[0].ResidualObjects = []string{"replacement file remains"}
	contents, err := yaml.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	metadataPath := filepath.Join(environment.dataHome, "devtask", "tasks", "billing.yaml")
	if err := os.WriteFile(metadataPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}

	report := readTaskStatus(t, environment)
	status := report.Attachments[0]
	if report.State != "incomplete" || !report.IncompleteFlag || report.Incomplete == nil || report.Incomplete.Operation != "add_repository" || report.Incomplete.LastError != "projection rollback failed" || len(report.Incomplete.ResidualObjects) != 1 || len(report.Incomplete.Recovery) != 1 || status.Clean || !status.Incomplete || status.LastError != "could not restore projection" || len(status.Residuals) != 1 {
		t.Fatalf("incomplete status = %#v", report)
	}
	human := environment.run(t, "status", "billing")
	for _, want := range []string{"incomplete", "projection rollback failed", "Task Workspace entry remains", "restore the Task Workspace entry", "could not restore projection", "replacement file remains"} {
		if !strings.Contains(human.stdout, want) {
			t.Fatalf("human incomplete status = %q, want %q", human.stdout, want)
		}
	}
}

func TestStatusReportsMissingTaskWorkspace(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	if result := environment.run(t, "new", "billing"); result.code != 0 {
		t.Fatalf("new failed: %s", result.stderr)
	}
	workspace := filepath.Join(environment.dataHome, "devtask", "workspaces", "billing")
	if err := os.Rename(workspace, workspace+"-moved"); err != nil {
		t.Fatal(err)
	}

	report := readTaskStatus(t, environment)
	if !report.Missing || report.Unknown || report.Inspection == nil || !strings.Contains(report.Inspection.Operation, "Task Workspace") || len(report.Inspection.Recovery) == 0 {
		t.Fatalf("missing Task status = %#v", report)
	}
	human := environment.run(t, "status", "billing")
	if human.code != 0 || !strings.Contains(human.stdout, "missing") || !strings.Contains(human.stdout, report.Inspection.Operation) {
		t.Fatalf("human missing Task status: code=%d stdout=%q stderr=%q", human.code, human.stdout, human.stderr)
	}
}

func TestStatusPreservesRepositoryAttachmentCreationOrder(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	for _, alias := range []string{"zeta", "alpha"} {
		repository := filepath.Join(t.TempDir(), alias)
		gitRun(t, "init", "-b", "main", repository)
		if err := os.WriteFile(filepath.Join(repository, "tracked.txt"), []byte(alias+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitRun(t, "-C", repository, "add", "tracked.txt")
		gitRun(t, "-C", repository, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "initial")
		if result := environment.run(t, "repo", "add", alias, repository); result.code != 0 {
			t.Fatalf("repo add %s failed: %s", alias, result.stderr)
		}
	}
	if result := environment.run(t, "new", "billing"); result.code != 0 {
		t.Fatalf("new failed: %s", result.stderr)
	}
	for _, alias := range []string{"zeta", "alpha"} {
		if result := environment.run(t, "add", "billing", alias, "--no-fetch"); result.code != 0 {
			t.Fatalf("add %s failed: %s", alias, result.stderr)
		}
	}

	report := readTaskStatus(t, environment)
	if len(report.Attachments) != 2 || report.Attachments[0].Alias != "zeta" || report.Attachments[1].Alias != "alpha" {
		t.Fatalf("attachment order = %#v", report.Attachments)
	}
	human := environment.run(t, "status", "billing")
	if zeta, alpha := strings.Index(human.stdout, "  zeta\t"), strings.Index(human.stdout, "  alpha\t"); zeta < 0 || alpha < 0 || zeta >= alpha {
		t.Fatalf("human attachment order = %q", human.stdout)
	}
}

func TestStatusReportsTaskContextAndWorkspaceLinkDrift(t *testing.T) {
	t.Run("missing Task Context File", func(t *testing.T) {
		environment := initializedCLIEnvironment(t)
		if result := environment.run(t, "new", "billing"); result.code != 0 {
			t.Fatalf("new failed: %s", result.stderr)
		}
		path := filepath.Join(environment.dataHome, "devtask", "workspaces", "billing", "TASK.md")
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}

		report := readTaskStatus(t, environment)
		if !report.Missing || report.Unknown || report.Inspection == nil || report.Inspection.Operation != "inspect Task Context File" || !strings.Contains(report.Inspection.Message, path) {
			t.Fatalf("missing Task Context File status = %#v", report)
		}
	})

	t.Run("unknown Task Workspace link inspection", func(t *testing.T) {
		environment, _, _ := createAttachedTask(t, "invoice")
		linkPath := filepath.Join(environment.dataHome, "devtask", "workspaces", "billing", "invoice")
		if err := os.Remove(linkPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("invoice", linkPath); err != nil {
			t.Fatal(err)
		}

		status := readTaskStatus(t, environment).Attachments[0]
		if status.Clean || status.Missing || !status.Unknown || status.Inspection == nil || status.Inspection.Operation != "resolve Task Workspace link" || status.Inspection.Message == "" || len(status.Inspection.Recovery) == 0 {
			t.Fatalf("unknown Task Workspace link status = %#v", status)
		}
	})
}

func TestStatusRejectsInvalidRequestsWithValidationExitCode(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	tests := [][]string{
		{"status"},
		{"status", "billing", "extra"},
		{"status", "bad/name"},
		{"status", "missing"},
	}
	for _, arguments := range tests {
		result := environment.run(t, arguments...)
		if result.code != 2 || result.stderr == "" {
			t.Fatalf("devtask %v: code=%d stderr=%q", arguments, result.code, result.stderr)
		}
	}
}

func TestStatusAbbreviatesHomePathsOnlyInHumanOutput(t *testing.T) {
	environment := newCLITestEnvironment(t)
	environment.configHome = filepath.Join(environment.home, ".config")
	environment.dataHome = filepath.Join(environment.home, ".local", "share")
	if result := environment.run(t, "init"); result.code != 0 {
		t.Fatalf("init failed: %s", result.stderr)
	}
	repository := filepath.Join(environment.home, "src", "invoice")
	gitRun(t, "init", "-b", "main", repository)
	if err := os.WriteFile(filepath.Join(repository, "tracked.txt"), []byte("tracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, "-C", repository, "add", "tracked.txt")
	gitRun(t, "-C", repository, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "initial")
	if result := environment.run(t, "repo", "add", "invoice", repository); result.code != 0 {
		t.Fatalf("repo add failed: %s", result.stderr)
	}
	if result := environment.run(t, "new", "billing"); result.code != 0 {
		t.Fatalf("new failed: %s", result.stderr)
	}
	if result := environment.run(t, "add", "billing", "invoice", "--no-fetch"); result.code != 0 {
		t.Fatalf("add failed: %s", result.stderr)
	}

	human := environment.run(t, "status", "billing")
	for _, want := range []string{"~/.local/share/devtask/workspaces/billing", "~/src/invoice/.worktrees/billing"} {
		if !strings.Contains(human.stdout, want) {
			t.Fatalf("human status = %q, want concise path %q", human.stdout, want)
		}
	}
	if strings.Contains(human.stdout, environment.home) {
		t.Fatalf("human status contains unabbreviated home path: %q", human.stdout)
	}
	machine := environment.run(t, "status", "billing", "--json")
	if !strings.Contains(machine.stdout, environment.home) || strings.Contains(machine.stdout, `"~`) {
		t.Fatalf("JSON status does not preserve absolute paths: %q", machine.stdout)
	}
}

func assertMissingStatus(t *testing.T, status attachmentStatusJSON, operation string) {
	t.Helper()
	if status.Clean || !status.Missing || status.Unknown || status.Inspection == nil || !strings.Contains(status.Inspection.Operation, operation) || status.Inspection.Message == "" || len(status.Inspection.Recovery) == 0 {
		t.Fatalf("missing attachment status = %#v", status)
	}
}

func createAttachedTask(t *testing.T, alias string) (cliTestEnvironment, string, string) {
	t.Helper()
	environment := initializedCLIEnvironment(t)
	repository := filepath.Join(t.TempDir(), alias+"-service")
	gitRun(t, "init", "-b", "main", repository)
	if err := os.WriteFile(filepath.Join(repository, "tracked.txt"), []byte("tracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, "-C", repository, "add", "tracked.txt")
	gitRun(t, "-C", repository, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "initial")
	if result := environment.run(t, "repo", "add", alias, repository); result.code != 0 {
		t.Fatalf("repo add failed: %s", result.stderr)
	}
	if result := environment.run(t, "new", "billing"); result.code != 0 {
		t.Fatalf("new failed: %s", result.stderr)
	}
	if result := environment.run(t, "add", "billing", alias, "--no-fetch"); result.code != 0 {
		t.Fatalf("add failed: %s", result.stderr)
	}
	worktree, err := filepath.EvalSymlinks(filepath.Join(repository, ".worktrees", "billing"))
	if err != nil {
		t.Fatal(err)
	}
	return environment, repository, worktree
}

func readTaskStatus(t *testing.T, environment cliTestEnvironment) taskStatusJSON {
	t.Helper()
	result := environment.run(t, "status", "billing", "--json")
	if result.code != 0 {
		t.Fatalf("status --json failed: code=%d stderr=%s", result.code, result.stderr)
	}
	var report taskStatusJSON
	if err := json.Unmarshal([]byte(result.stdout), &report); err != nil {
		t.Fatalf("decode status JSON: %v\n%s", err, result.stdout)
	}
	return report
}
