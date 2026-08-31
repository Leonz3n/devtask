package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Leonz3n/devtask/internal/config"
	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"
)

func TestRemoveRepoRemovesCleanAttachmentAndRetainsTaskBranch(t *testing.T) {
	environment, repository, worktree := createAttachedTask(t, "invoice")
	mainBranch := gitRun(t, "-C", repository, "branch", "--show-current")
	mainStatus := gitRun(t, "-C", repository, "status", "--porcelain")
	workspace := filepath.Join(environment.dataHome, "devtask", "workspaces", "billing")

	result := environment.run(t, "remove-repo", "billing", "invoice")

	if result.code != 0 {
		t.Fatalf("remove-repo failed: code=%d stderr=%q", result.code, result.stderr)
	}
	if !strings.Contains(result.stdout, "removed Task Worktree") || !strings.Contains(result.stdout, "retained Task Branch Name feat/billing") {
		t.Fatalf("remove-repo stdout = %q", result.stdout)
	}
	if _, err := os.Lstat(worktree); !os.IsNotExist(err) {
		t.Fatalf("Task Worktree remains: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(workspace, "invoice")); !os.IsNotExist(err) {
		t.Fatalf("Task Workspace link remains: %v", err)
	}
	agents, err := os.ReadFile(filepath.Join(workspace, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(agents), "`invoice`") || !strings.Contains(string(agents), "None.") {
		t.Fatalf("AGENTS.md still lists removed attachment:\n%s", agents)
	}
	metadata := readPersistedTask(t, environment, "billing")
	if len(metadata.Attachments) != 0 {
		t.Fatalf("Repository Attachment metadata remains: %#v", metadata.Attachments)
	}
	if branch := gitRun(t, "-C", repository, "branch", "--list", "feat/billing"); !strings.Contains(branch, "feat/billing") {
		t.Fatalf("Task Branch Name was deleted: %q", branch)
	}
	if branch := gitRun(t, "-C", repository, "branch", "--show-current"); branch != mainBranch {
		t.Fatalf("Main Checkout branch = %q, want %q", branch, mainBranch)
	}
	if status := gitRun(t, "-C", repository, "status", "--porcelain"); status != mainStatus {
		t.Fatalf("Main Checkout status = %q, want %q", status, mainStatus)
	}
}

func TestRemoveRepoProtectsEveryGitStatus(t *testing.T) {
	tests := []struct {
		name   string
		change func(*testing.T, string, string)
		want   string
	}{
		{
			name: "modified",
			change: func(t *testing.T, _, worktree string) {
				if err := os.WriteFile(filepath.Join(worktree, "tracked.txt"), []byte("modified\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			want: "modified",
		},
		{
			name: "staged",
			change: func(t *testing.T, _, worktree string) {
				if err := os.WriteFile(filepath.Join(worktree, "staged.txt"), []byte("staged\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				gitRun(t, "-C", worktree, "add", "staged.txt")
			},
			want: "staged",
		},
		{
			name: "untracked",
			change: func(t *testing.T, _, worktree string) {
				if err := os.WriteFile(filepath.Join(worktree, "untracked.txt"), []byte("untracked\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			want: "untracked",
		},
		{
			name:   "conflicted",
			change: createRemovalConflict,
			want:   "conflicted",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment, repository, worktree := createAttachedTask(t, "invoice")
			test.change(t, repository, worktree)

			result := environment.run(t, "remove-repo", "billing", "invoice")

			if result.code != 2 || !strings.Contains(result.stderr, test.want) || !strings.Contains(result.stderr, "--force") {
				t.Fatalf("protected %s result: code=%d stderr=%q", test.name, result.code, result.stderr)
			}
			if _, err := os.Lstat(worktree); err != nil {
				t.Fatalf("protected Task Worktree changed: %v", err)
			}
			if len(readPersistedTask(t, environment, "billing").Attachments) != 1 {
				t.Fatal("protected Repository Attachment metadata changed")
			}
		})
	}
}

func TestRemoveRepoProtectsUnknownIgnoredContentButExemptsExactManagedLink(t *testing.T) {
	t.Run("unknown ignored content", func(t *testing.T) {
		environment, repository, worktree := createAttachedTask(t, "invoice")
		excludePath := filepath.Join(repository, ".git", "info", "exclude")
		exclude, err := os.OpenFile(excludePath, os.O_APPEND|os.O_WRONLY, 0)
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
		if err := os.WriteFile(filepath.Join(worktree, "local.cache"), []byte("valuable\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		result := environment.run(t, "remove-repo", "billing", "invoice")

		if result.code != 2 || !strings.Contains(result.stderr, "unknown ignored local.cache") {
			t.Fatalf("unknown ignored result: code=%d stderr=%q", result.code, result.stderr)
		}
		if contents, err := os.ReadFile(filepath.Join(worktree, "local.cache")); err != nil || string(contents) != "valuable\n" {
			t.Fatalf("unknown ignored data changed: %q, %v", contents, err)
		}
	})

	t.Run("exact managed link", func(t *testing.T) {
		environment, repository, worktree, destination := createAttachedTaskWithManagedLink(t)

		result := environment.run(t, "remove-repo", "billing", "invoice")

		if result.code != 0 {
			t.Fatalf("remove with exact managed link failed: code=%d stderr=%q", result.code, result.stderr)
		}
		if _, err := os.Lstat(destination); !os.IsNotExist(err) {
			t.Fatalf("managed link remains with removed Task Worktree: %v", err)
		}
		if source, err := os.ReadFile(filepath.Join(repository, ".env")); err != nil || string(source) != "secret\n" {
			t.Fatalf("Shared Local Path source changed: %q, %v", source, err)
		}
		if _, err := os.Lstat(worktree); !os.IsNotExist(err) {
			t.Fatalf("Task Worktree remains: %v", err)
		}
	})
}

func TestRemoveRepoProtectsChangedManagedLinksAndForceAuthorizesContentOnly(t *testing.T) {
	for _, replacement := range []struct {
		name    string
		install func(*testing.T, string)
	}{
		{name: "file", install: func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("replacement\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "directory", install: func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "redirected link", install: func(t *testing.T, path string) {
			if err := os.Symlink("elsewhere", path); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(replacement.name, func(t *testing.T) {
			environment, _, worktree, destination := createAttachedTaskWithManagedLink(t)
			if err := os.Remove(destination); err != nil {
				t.Fatal(err)
			}
			replacement.install(t, destination)

			refused := environment.run(t, "remove-repo", "billing", "invoice")
			if refused.code != 2 || !strings.Contains(refused.stderr, "changed Shared Local Path .env") {
				t.Fatalf("changed managed link result: code=%d stderr=%q", refused.code, refused.stderr)
			}
			forced := environment.run(t, "remove-repo", "billing", "invoice", "--force")
			if forced.code != 0 {
				t.Fatalf("forced remove failed: code=%d stderr=%q", forced.code, forced.stderr)
			}
			if _, err := os.Lstat(worktree); !os.IsNotExist(err) {
				t.Fatalf("forced removal left Task Worktree: %v", err)
			}
		})
	}
}

func TestRemoveRepoForceNeverBypassesMainCheckoutOrContainmentIdentity(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*testing.T, cliTestEnvironment, string)
		want   string
	}{
		{
			name: "Main Checkout",
			change: func(t *testing.T, environment cliTestEnvironment, repository string) {
				metadata := readPersistedTask(t, environment, "billing")
				metadata.Attachments[0].WorktreePath = metadata.Attachments[0].MainCheckout
				writePersistedTask(t, environment, metadata)
			},
			want: "Main Checkout",
		},
		{
			name: "outside containment",
			change: func(t *testing.T, environment cliTestEnvironment, _ string) {
				metadata := readPersistedTask(t, environment, "billing")
				metadata.Attachments[0].WorktreePath = filepath.Join(t.TempDir(), "outside")
				writePersistedTask(t, environment, metadata)
			},
			want: "exact managed path",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			environment, repository, worktree := createAttachedTask(t, "invoice")
			test.change(t, environment, repository)

			result := environment.run(t, "remove-repo", "billing", "invoice", "--force")

			if result.code != 2 || !strings.Contains(result.stderr, test.want) {
				t.Fatalf("identity result: code=%d stderr=%q", result.code, result.stderr)
			}
			if _, err := os.Lstat(worktree); err != nil {
				t.Fatalf("identity refusal changed Task Worktree: %v", err)
			}
			if branch := gitRun(t, "-C", repository, "branch", "--show-current"); branch != "main" {
				t.Fatalf("identity refusal changed Main Checkout: %q", branch)
			}
		})
	}
}

func TestRemoveRepoDeletesOnlyMergedTaskBranchWhenExplicitlyRequested(t *testing.T) {
	environment, repository, worktree := createAttachedTask(t, "invoice")

	result := environment.run(t, "remove-repo", "billing", "invoice", "--delete-branch", "--no-fetch")

	if result.code != 0 {
		t.Fatalf("safe branch removal failed: code=%d stderr=%q", result.code, result.stderr)
	}
	if !strings.Contains(result.stdout, "removed Task Worktree") || !strings.Contains(result.stdout, "deleted Task Branch Name feat/billing") {
		t.Fatalf("safe branch removal stdout = %q", result.stdout)
	}
	if _, err := os.Lstat(worktree); !os.IsNotExist(err) {
		t.Fatalf("Task Worktree remains: %v", err)
	}
	if branch := gitRun(t, "-C", repository, "branch", "--list", "feat/billing"); branch != "" {
		t.Fatalf("merged Task Branch Name remains: %q", branch)
	}
}

func TestRemoveRepoRequiresBothFlagsToDeleteUnmergedTaskBranch(t *testing.T) {
	environment, repository, worktree := createAttachedTask(t, "invoice")
	if err := os.WriteFile(filepath.Join(worktree, "task.txt"), []byte("task change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, "-C", worktree, "add", "task.txt")
	gitRun(t, "-C", worktree, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "task change")

	refused := environment.run(t, "remove-repo", "billing", "invoice", "--delete-branch", "--no-fetch")
	if refused.code != 2 || !strings.Contains(refused.stderr, "not fully merged") || !strings.Contains(refused.stderr, "both --delete-branch and --force") {
		t.Fatalf("unmerged refusal: code=%d stderr=%q", refused.code, refused.stderr)
	}
	if _, err := os.Lstat(worktree); err != nil {
		t.Fatalf("unmerged refusal changed Task Worktree: %v", err)
	}
	if branch := gitRun(t, "-C", repository, "branch", "--list", "feat/billing"); branch == "" {
		t.Fatal("unmerged refusal deleted Task Branch Name")
	}

	forced := environment.run(t, "remove-repo", "billing", "invoice", "--delete-branch", "--force", "--no-fetch")
	if forced.code != 0 || !strings.Contains(forced.stdout, "deleted Task Branch Name feat/billing") {
		t.Fatalf("forced unmerged deletion failed: code=%d stdout=%q stderr=%q", forced.code, forced.stdout, forced.stderr)
	}
	if branch := gitRun(t, "-C", repository, "branch", "--list", "feat/billing"); branch != "" {
		t.Fatalf("forced unmerged Task Branch Name remains: %q", branch)
	}
}

func TestRemoveRepoForceWithoutDeleteBranchAlwaysRetainsTaskBranch(t *testing.T) {
	environment, repository, worktree := createAttachedTask(t, "invoice")
	if err := os.WriteFile(filepath.Join(worktree, "valuable.txt"), []byte("authorized deletion\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := environment.run(t, "remove-repo", "billing", "invoice", "--force")

	if result.code != 0 || !strings.Contains(result.stdout, "retained Task Branch Name feat/billing") {
		t.Fatalf("forced content removal failed: code=%d stdout=%q stderr=%q", result.code, result.stdout, result.stderr)
	}
	if branch := gitRun(t, "-C", repository, "branch", "--list", "feat/billing"); branch == "" {
		t.Fatal("--force without --delete-branch deleted Task Branch Name")
	}
}

func TestRemoveRepoDeleteBranchRequiresSuccessfulFetchUnlessOfflineIsExplicit(t *testing.T) {
	environment, repository, worktree := createAttachedTask(t, "invoice")
	gitRun(t, "-C", repository, "remote", "add", "origin", filepath.Join(t.TempDir(), "unavailable.git"))

	online := environment.run(t, "remove-repo", "billing", "invoice", "--delete-branch")
	if online.code != 1 || !strings.Contains(online.stderr, "fetch configured remote") {
		t.Fatalf("failed fetch result: code=%d stderr=%q", online.code, online.stderr)
	}
	if _, err := os.Lstat(worktree); err != nil {
		t.Fatalf("failed fetch changed Task Worktree: %v", err)
	}
	if branch := gitRun(t, "-C", repository, "branch", "--list", "feat/billing"); branch == "" {
		t.Fatal("failed fetch deleted Task Branch Name")
	}

	offline := environment.run(t, "remove-repo", "billing", "invoice", "--delete-branch", "--no-fetch")
	if offline.code != 0 {
		t.Fatalf("explicit offline removal failed: code=%d stderr=%q", offline.code, offline.stderr)
	}
	if branch := gitRun(t, "-C", repository, "branch", "--list", "feat/billing"); branch != "" {
		t.Fatalf("offline removal left Task Branch Name: %q", branch)
	}
}

func TestRemoveRepoForgetCleansOnlyAlreadyAbsentExternalState(t *testing.T) {
	environment, repository, worktree := createAttachedTask(t, "invoice")
	gitRun(t, "-C", repository, "worktree", "remove", "--force", worktree)
	workspace := filepath.Join(environment.dataHome, "devtask", "workspaces", "billing")

	result := environment.run(t, "remove-repo", "billing", "invoice", "--forget")

	if result.code != 0 || !strings.Contains(result.stdout, "forgot Repository Attachment invoice") || strings.Contains(result.stdout, "deleted Task Branch") {
		t.Fatalf("forget result: code=%d stdout=%q stderr=%q", result.code, result.stdout, result.stderr)
	}
	if branch := gitRun(t, "-C", repository, "branch", "--list", "feat/billing"); branch == "" {
		t.Fatal("--forget touched Task Branch Name")
	}
	if len(readPersistedTask(t, environment, "billing").Attachments) != 0 {
		t.Fatal("--forget left Repository Attachment metadata")
	}
	if _, err := os.Lstat(filepath.Join(workspace, "invoice")); !os.IsNotExist(err) {
		t.Fatalf("--forget left Task Workspace link: %v", err)
	}
	agents, err := os.ReadFile(filepath.Join(workspace, "AGENTS.md"))
	if err != nil || !strings.Contains(string(agents), "None.") {
		t.Fatalf("--forget did not update AGENTS.md: %q, %v", agents, err)
	}
}

func TestRemoveRepoForgetRejectsPartialPresenceAndForbiddenFlags(t *testing.T) {
	t.Run("path and record present", func(t *testing.T) {
		environment, _, worktree := createAttachedTask(t, "invoice")
		result := environment.run(t, "remove-repo", "billing", "invoice", "--forget")
		if result.code != 2 || !strings.Contains(result.stderr, "path") || !strings.Contains(result.stderr, "absent") {
			t.Fatalf("present path forget result: code=%d stderr=%q", result.code, result.stderr)
		}
		if _, err := os.Lstat(worktree); err != nil {
			t.Fatalf("refused forget changed Task Worktree: %v", err)
		}
	})

	t.Run("record remains", func(t *testing.T) {
		environment, _, worktree := createAttachedTask(t, "invoice")
		if err := os.RemoveAll(worktree); err != nil {
			t.Fatal(err)
		}
		result := environment.run(t, "remove-repo", "billing", "invoice", "--forget")
		if result.code != 2 || !strings.Contains(result.stderr, "Git worktree record") || !strings.Contains(result.stderr, "absent") {
			t.Fatalf("remaining record forget result: code=%d stderr=%q", result.code, result.stderr)
		}
		if len(readPersistedTask(t, environment, "billing").Attachments) != 1 {
			t.Fatal("refused forget changed metadata")
		}
	})

	t.Run("path reappeared", func(t *testing.T) {
		environment, repository, worktree := createAttachedTask(t, "invoice")
		gitRun(t, "-C", repository, "worktree", "remove", "--force", worktree)
		if err := os.Mkdir(worktree, 0o700); err != nil {
			t.Fatal(err)
		}
		result := environment.run(t, "remove-repo", "billing", "invoice", "--forget")
		if result.code != 2 || !strings.Contains(result.stderr, "path") || !strings.Contains(result.stderr, "absent") {
			t.Fatalf("reappeared path forget result: code=%d stderr=%q", result.code, result.stderr)
		}
	})

	t.Run("branch deletion combination", func(t *testing.T) {
		environment := initializedCLIEnvironment(t)
		result := environment.run(t, "remove-repo", "billing", "invoice", "--forget", "--delete-branch")
		if result.code != 2 || !strings.Contains(result.stderr, "mutually exclusive") {
			t.Fatalf("forbidden flags result: code=%d stderr=%q", result.code, result.stderr)
		}
	})
}

func TestRemoveRepoUsesCurrentBaseRefForSafeBranchDeletion(t *testing.T) {
	environment, repository, worktree := createAttachedTask(t, "invoice")
	if err := os.WriteFile(filepath.Join(worktree, "task.txt"), []byte("merged later\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, "-C", worktree, "add", "task.txt")
	gitRun(t, "-C", worktree, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "task change")
	gitRun(t, "-C", repository, "-c", "user.name=Test", "-c", "user.email=test@example.com", "merge", "--no-ff", "feat/billing", "-m", "merge task")

	result := environment.run(t, "remove-repo", "billing", "invoice", "--delete-branch", "--no-fetch")

	if result.code != 0 || !strings.Contains(result.stdout, "deleted Task Branch Name feat/billing") {
		t.Fatalf("current Base Ref deletion failed: code=%d stdout=%q stderr=%q", result.code, result.stdout, result.stderr)
	}
	if branch := gitRun(t, "-C", repository, "branch", "--list", "feat/billing"); branch != "" {
		t.Fatalf("merged Task Branch Name remains: %q", branch)
	}
}

func TestRemoveRepoRejectsChangedGitAndWorkspaceIdentityEvenWithForce(t *testing.T) {
	t.Run("Task Branch Name", func(t *testing.T) {
		environment, repository, worktree := createAttachedTask(t, "invoice")
		gitRun(t, "-C", worktree, "checkout", "--detach")

		result := environment.run(t, "remove-repo", "billing", "invoice", "--force")

		if result.code != 2 || !strings.Contains(result.stderr, "Git worktree record") || !strings.Contains(result.stderr, "Task Branch Name") {
			t.Fatalf("changed branch result: code=%d stderr=%q", result.code, result.stderr)
		}
		if _, err := os.Lstat(worktree); err != nil {
			t.Fatalf("changed branch Task Worktree removed: %v", err)
		}
		if branch := gitRun(t, "-C", repository, "branch", "--list", "feat/billing"); branch == "" {
			t.Fatal("changed branch check deleted Task Branch Name")
		}
	})

	t.Run("Git worktree record", func(t *testing.T) {
		environment, repository, worktree := createAttachedTask(t, "invoice")
		gitRun(t, "-C", repository, "worktree", "remove", "--force", worktree)
		if err := os.Mkdir(worktree, 0o700); err != nil {
			t.Fatal(err)
		}

		result := environment.run(t, "remove-repo", "billing", "invoice", "--force")

		if result.code != 2 || !strings.Contains(result.stderr, "Git worktree record") {
			t.Fatalf("missing record result: code=%d stderr=%q", result.code, result.stderr)
		}
		if info, err := os.Lstat(worktree); err != nil || !info.IsDir() {
			t.Fatalf("unowned replacement path changed: %v, %v", info, err)
		}
	})

	t.Run("Task Workspace link", func(t *testing.T) {
		environment, _, worktree := createAttachedTask(t, "invoice")
		link := filepath.Join(environment.dataHome, "devtask", "workspaces", "billing", "invoice")
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join("..", "elsewhere"), link); err != nil {
			t.Fatal(err)
		}

		result := environment.run(t, "remove-repo", "billing", "invoice", "--force")

		if result.code != 2 || !strings.Contains(result.stderr, "Task Workspace") || !strings.Contains(result.stderr, "link target changed") {
			t.Fatalf("changed workspace link result: code=%d stderr=%q", result.code, result.stderr)
		}
		if _, err := os.Lstat(worktree); err != nil {
			t.Fatalf("changed workspace link removal changed Task Worktree: %v", err)
		}
	})
}

func TestRemoveRepoUnknownGitInspectionBlocksRemovalEvenWithForce(t *testing.T) {
	environment, repository, worktree := createAttachedTask(t, "invoice")
	gitDirectory := filepath.Join(repository, ".git")
	away := filepath.Join(repository, ".git-away")
	if err := os.Rename(gitDirectory, away); err != nil {
		t.Fatal(err)
	}

	result := environment.run(t, "remove-repo", "billing", "invoice", "--force")

	if result.code != 1 || !strings.Contains(result.stderr, "locate Registered Repository lock") {
		t.Fatalf("unknown Git inspection result: code=%d stderr=%q", result.code, result.stderr)
	}
	if _, err := os.Lstat(worktree); err != nil {
		t.Fatalf("unknown Git inspection changed Task Worktree: %v", err)
	}
}

func TestRemoveRepoHonorsTaskAndRepositoryLocksEvenWithForce(t *testing.T) {
	for _, test := range []struct {
		name     string
		lockPath func(cliTestEnvironment, string) string
		want     string
	}{
		{
			name: "Task lock",
			lockPath: func(environment cliTestEnvironment, _ string) string {
				return filepath.Join(environment.dataHome, "devtask", "tasks", ".billing.lock")
			},
			want: "Task \"billing\" is busy",
		},
		{
			name: "Registered Repository lock",
			lockPath: func(_ cliTestEnvironment, repository string) string {
				return filepath.Join(repository, ".git", "devtask.lock")
			},
			want: "Registered Repository \"invoice\" is busy",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			environment, repository, worktree := createAttachedTask(t, "invoice")
			file, err := os.OpenFile(test.lockPath(environment, repository), os.O_CREATE|os.O_RDWR, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
				t.Fatal(err)
			}

			result := environment.run(t, "remove-repo", "billing", "invoice", "--force")

			if result.code != 1 || !strings.Contains(result.stderr, test.want) {
				t.Fatalf("busy lock result: code=%d stderr=%q", result.code, result.stderr)
			}
			if _, err := os.Lstat(worktree); err != nil {
				t.Fatalf("busy lock changed Task Worktree: %v", err)
			}
		})
	}
}

func TestRemoveRepoPersistsPartialRemovalAndForgetRecovers(t *testing.T) {
	environment, repository, worktree := createAttachedTask(t, "invoice")
	binary := devtaskBinaryWithTags(t, "devtask_test")
	command := exec.Command(binary, "remove-repo", "billing", "invoice")
	command.Dir = environment.home
	command.Env = append(filteredEnvironment(os.Environ()),
		"HOME="+environment.home,
		"XDG_CONFIG_HOME="+environment.configHome,
		"XDG_DATA_HOME="+environment.dataHome,
		"DEVTASK_TEST_FAIL_AFTER_WORKTREE_REMOVAL=1",
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("injected partial removal succeeded: %s", output)
	}
	exitError, ok := err.(*exec.ExitError)
	if !ok || exitError.ExitCode() != 1 {
		t.Fatalf("partial removal error = %v, output=%s", err, output)
	}
	for _, want := range []string{"removed Task Worktree", "injected failure", "incomplete", "--forget"} {
		if !strings.Contains(string(output), want) {
			t.Fatalf("partial removal output = %q, want %q", output, want)
		}
	}
	if _, err := os.Lstat(worktree); !os.IsNotExist(err) {
		t.Fatalf("partial removal did not remove Task Worktree: %v", err)
	}
	metadata := readPersistedTask(t, environment, "billing")
	if metadata.State != "incomplete" || metadata.Incomplete == nil || len(metadata.Attachments) != 1 || metadata.Attachments[0].State != "incomplete" {
		t.Fatalf("partial removal metadata = %#v", metadata)
	}
	for _, observed := range []string{"Task Worktree path is absent", "Git worktree record is absent", "Task Branch Name remains", "Task Workspace link remains", "generated AGENTS entry remains"} {
		joined := strings.Join(metadata.Incomplete.ResidualObjects, "\n")
		if !strings.Contains(joined, observed) {
			t.Fatalf("partial removal residuals = %#v, want %q", metadata.Incomplete.ResidualObjects, observed)
		}
	}
	if branch := gitRun(t, "-C", repository, "branch", "--list", "feat/billing"); branch == "" {
		t.Fatal("partial removal touched retained Task Branch Name")
	}

	recovered := environment.run(t, "remove-repo", "billing", "invoice", "--forget")
	if recovered.code != 0 {
		t.Fatalf("--forget recovery failed: code=%d stderr=%q", recovered.code, recovered.stderr)
	}
	metadata = readPersistedTask(t, environment, "billing")
	if metadata.State != "ready" || metadata.Incomplete != nil || len(metadata.Attachments) != 0 {
		t.Fatalf("recovered Task metadata = %#v", metadata)
	}
}

func TestRemoveRepoRejectsCorruptAttachmentAliasBeforeWorkspaceMutation(t *testing.T) {
	environment, _, worktree := createAttachedTask(t, "invoice")
	metadata := readPersistedTask(t, environment, "billing")
	metadata.Attachments[0].Alias = "../outside-link"
	writePersistedTask(t, environment, metadata)
	workspace := filepath.Join(environment.dataHome, "devtask", "workspaces", "billing")
	outside := filepath.Join(workspace, "..", "outside-link")
	if err := os.Symlink(worktree, outside); err != nil {
		t.Fatal(err)
	}

	result := environment.run(t, "remove-repo", "billing", "../outside-link", "--force")

	if result.code != 2 || !strings.Contains(result.stderr, "invalid Repository Alias") {
		t.Fatalf("corrupt alias result: code=%d stderr=%q", result.code, result.stderr)
	}
	if target, err := os.Readlink(outside); err != nil || target != worktree {
		t.Fatalf("outside symlink changed: target=%q error=%v", target, err)
	}
	if _, err := os.Lstat(worktree); err != nil {
		t.Fatalf("corrupt alias changed Task Worktree: %v", err)
	}
}

func TestRemoveRepoForgetPreservesUnrelatedIncompleteRecovery(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	first := createCommittedRepository(t, "invoice")
	second := createCommittedRepository(t, "ledger")
	for alias, repository := range map[string]string{"invoice": first, "ledger": second} {
		if result := environment.run(t, "repo", "add", alias, repository); result.code != 0 {
			t.Fatalf("repo add %s failed: %s", alias, result.stderr)
		}
	}
	if result := environment.run(t, "new", "billing"); result.code != 0 {
		t.Fatalf("new failed: %s", result.stderr)
	}
	if result := environment.run(t, "add", "billing", "invoice", "ledger", "--no-fetch"); result.code != 0 {
		t.Fatalf("add failed: %s", result.stderr)
	}
	metadata := readPersistedTask(t, environment, "billing")
	metadata.State = "incomplete"
	metadata.Attachments[1].State = "incomplete"
	metadata.Attachments[1].LastError = "ledger recovery needed"
	metadata.Incomplete = &struct {
		Operation       string   `yaml:"operation"`
		LastError       string   `yaml:"last_error"`
		ResidualObjects []string `yaml:"residual_objects"`
		Recovery        []string `yaml:"recovery"`
	}{Operation: "add_repository", LastError: "ledger recovery needed", ResidualObjects: []string{"ledger residual"}, Recovery: []string{"repair ledger"}}
	writePersistedTask(t, environment, metadata)
	firstWorktree := filepath.Join(first, ".worktrees", "billing")
	gitRun(t, "-C", first, "worktree", "remove", "--force", firstWorktree)

	result := environment.run(t, "remove-repo", "billing", "invoice", "--forget")

	if result.code != 0 {
		t.Fatalf("forget with unrelated incomplete state failed: code=%d stderr=%q", result.code, result.stderr)
	}
	metadata = readPersistedTask(t, environment, "billing")
	if metadata.State != "incomplete" || metadata.Incomplete == nil || metadata.Incomplete.LastError != "ledger recovery needed" || len(metadata.Attachments) != 1 || metadata.Attachments[0].Alias != "ledger" || metadata.Attachments[0].State != "incomplete" {
		t.Fatalf("unrelated incomplete recovery was lost: %#v", metadata)
	}
}

func TestRemoveRepoUpdatesOnlySelectedAttachmentAndPreservesManualAgentsContent(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	first := createCommittedRepository(t, "invoice")
	second := createCommittedRepository(t, "ledger")
	for alias, repository := range map[string]string{"invoice": first, "ledger": second} {
		if result := environment.run(t, "repo", "add", alias, repository); result.code != 0 {
			t.Fatalf("repo add %s failed: %s", alias, result.stderr)
		}
	}
	if result := environment.run(t, "new", "billing"); result.code != 0 {
		t.Fatalf("new failed: %s", result.stderr)
	}
	if result := environment.run(t, "add", "billing", "invoice", "ledger", "--no-fetch"); result.code != 0 {
		t.Fatalf("add failed: %s", result.stderr)
	}
	workspace := filepath.Join(environment.dataHome, "devtask", "workspaces", "billing")
	agentsPath := filepath.Join(workspace, "AGENTS.md")
	agents, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatal(err)
	}
	manual := "# Human notes\n\nKeep this text.\n\n"
	if err := os.WriteFile(agentsPath, append([]byte(manual), agents...), 0o600); err != nil {
		t.Fatal(err)
	}

	result := environment.run(t, "remove-repo", "billing", "INVOICE")

	if result.code != 0 {
		t.Fatalf("selected removal failed: code=%d stderr=%q", result.code, result.stderr)
	}
	updated, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(updated), manual) || strings.Contains(string(updated), "`invoice`") || !strings.Contains(string(updated), "`ledger`") {
		t.Fatalf("AGENTS.md projection is wrong:\n%s", updated)
	}
	if _, err := os.Lstat(filepath.Join(workspace, "invoice")); !os.IsNotExist(err) {
		t.Fatalf("removed Task Workspace link remains: %v", err)
	}
	wantRemaining, err := filepath.EvalSymlinks(filepath.Join(second, ".worktrees", "billing"))
	if err != nil {
		t.Fatal(err)
	}
	if resolved, err := filepath.EvalSymlinks(filepath.Join(workspace, "ledger")); err != nil || resolved != wantRemaining {
		t.Fatalf("remaining Task Workspace link = %q, %v", resolved, err)
	}
	metadata := readPersistedTask(t, environment, "billing")
	if len(metadata.Attachments) != 1 || metadata.Attachments[0].Alias != "ledger" || metadata.Attachments[0].Order != 1 {
		t.Fatalf("remaining Repository Attachments = %#v", metadata.Attachments)
	}
	if _, err := os.Lstat(filepath.Join(second, ".worktrees", "billing")); err != nil {
		t.Fatalf("remaining Task Worktree changed: %v", err)
	}
}

func TestRemoveRepoValidatesRequestsWithoutMutation(t *testing.T) {
	environment, _, worktree := createAttachedTask(t, "invoice")
	tests := []struct {
		arguments []string
		want      string
	}{
		{arguments: []string{"remove-repo"}, want: "accepts 2 arg"},
		{arguments: []string{"remove-repo", "billing", "invoice", "extra"}, want: "accepts 2 arg"},
		{arguments: []string{"remove-repo", "bad/name", "invoice"}, want: "invalid Task name"},
		{arguments: []string{"remove-repo", "missing", "invoice"}, want: "does not exist"},
		{arguments: []string{"remove-repo", "billing", "missing"}, want: "no Repository Attachment"},
		{arguments: []string{"remove-repo", "billing", "invoice", "--fetch", "--no-fetch"}, want: "mutually exclusive"},
	}
	for _, test := range tests {
		result := environment.run(t, test.arguments...)
		if result.code != 2 || !strings.Contains(result.stderr, test.want) {
			t.Fatalf("devtask %v: code=%d stderr=%q, want %q", test.arguments, result.code, result.stderr, test.want)
		}
	}
	if _, err := os.Lstat(worktree); err != nil {
		t.Fatalf("invalid requests changed Task Worktree: %v", err)
	}
	if len(readPersistedTask(t, environment, "billing").Attachments) != 1 {
		t.Fatal("invalid requests changed Repository Attachment metadata")
	}
}

func createRemovalConflict(t *testing.T, repository, worktree string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(worktree, "tracked.txt"), []byte("task\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, "-C", worktree, "add", "tracked.txt")
	gitRun(t, "-C", worktree, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "task change")
	if err := os.WriteFile(filepath.Join(repository, "tracked.txt"), []byte("main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, "-C", repository, "add", "tracked.txt")
	gitRun(t, "-C", repository, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "main change")
	command := exec.Command("git", "-C", worktree, "merge", "main")
	if output, err := command.CombinedOutput(); err == nil {
		t.Fatalf("merge unexpectedly succeeded: %s", output)
	}
}

func createAttachedTaskWithManagedLink(t *testing.T) (cliTestEnvironment, string, string, string) {
	t.Helper()
	environment := initializedCLIEnvironment(t)
	repository := createCommittedRepository(t, "invoice")
	if err := os.WriteFile(filepath.Join(repository, ".gitignore"), []byte(".env\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, "-C", repository, "add", ".gitignore")
	gitRun(t, "-C", repository, "commit", "-m", "ignore local environment")
	if err := os.WriteFile(filepath.Join(repository, ".env"), []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if result := environment.run(t, "repo", "add", "invoice", repository); result.code != 0 {
		t.Fatalf("repo add failed: %s", result.stderr)
	}
	updateConfiguration(t, environment, func(configuration *config.Config) {
		repositoryConfiguration := configuration.Repositories["invoice"]
		repositoryConfiguration.SharedPaths = []string{".env"}
		configuration.Repositories["invoice"] = repositoryConfiguration
	})
	if result := environment.run(t, "new", "billing"); result.code != 0 {
		t.Fatalf("new failed: %s", result.stderr)
	}
	if result := environment.run(t, "add", "billing", "invoice", "--no-fetch"); result.code != 0 {
		t.Fatalf("add failed: %s", result.stderr)
	}
	worktree := filepath.Join(repository, ".worktrees", "billing")
	return environment, repository, worktree, filepath.Join(worktree, ".env")
}

func writePersistedTask(t *testing.T, environment cliTestEnvironment, metadata persistedTask) {
	t.Helper()
	contents, err := yaml.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(environment.dataHome, "devtask", "tasks", metadata.Name+".yaml"), contents, 0o600); err != nil {
		t.Fatal(err)
	}
}
