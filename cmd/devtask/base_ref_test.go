package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Leonz3n/devtask/internal/config"
	"gopkg.in/yaml.v3"
)

func TestAddFetchesAndPrefersRemoteTrackingBaseRef(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	fixture := createAdvancedRemoteRepository(t)
	gitRun(t, "-C", fixture.repository, "remote", "rename", "origin", "upstream")

	if result := environment.run(t, "repo", "add", "service", fixture.repository); result.code != 0 {
		t.Fatalf("repo add failed: %s", result.stderr)
	}
	updateConfiguration(t, environment, func(configuration *config.Config) {
		repositoryConfiguration := configuration.Repositories["service"]
		repositoryConfiguration.Remote = "upstream"
		configuration.Repositories["service"] = repositoryConfiguration
	})
	if result := environment.run(t, "new", "billing"); result.code != 0 {
		t.Fatalf("new failed: %s", result.stderr)
	}
	result := environment.run(t, "add", "billing", "service")
	if result.code != 0 {
		t.Fatalf("add failed: code=%d stderr=%s", result.code, result.stderr)
	}

	worktree := filepath.Join(fixture.repository, ".worktrees", "billing")
	if commit := gitRun(t, "-C", worktree, "rev-parse", "HEAD^{commit}"); commit != fixture.freshCommit {
		t.Fatalf("Task Worktree commit = %s, want freshly fetched remote commit %s", commit, fixture.freshCommit)
	}
	if commit := gitRun(t, "-C", fixture.repository, "rev-parse", "refs/heads/main^{commit}"); commit != fixture.staleCommit {
		t.Fatalf("local main moved to %s, want unchanged %s", commit, fixture.staleCommit)
	}
	metadata := readPersistedTask(t, environment, "billing")
	if len(metadata.Attachments) != 1 {
		t.Fatalf("attachments = %#v, want one", metadata.Attachments)
	}
	attachment := metadata.Attachments[0]
	if attachment.BaseRef != "refs/remotes/upstream/main" || attachment.BaseCommit != fixture.freshCommit {
		t.Fatalf("Base Ref snapshot = %q at %q, want refs/remotes/upstream/main at %q", attachment.BaseRef, attachment.BaseCommit, fixture.freshCommit)
	}
}

func TestAddFetchFlagsOverrideConfiguredBehavior(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	fixture := createAdvancedRemoteRepository(t)

	if result := environment.run(t, "repo", "add", "service", fixture.repository); result.code != 0 {
		t.Fatalf("repo add failed: %s", result.stderr)
	}
	if result := environment.run(t, "new", "offline"); result.code != 0 {
		t.Fatalf("new offline failed: %s", result.stderr)
	}
	offline := environment.run(t, "add", "offline", "service", "--no-fetch")
	if offline.code != 0 {
		t.Fatalf("add --no-fetch failed: code=%d stderr=%s", offline.code, offline.stderr)
	}
	if commit := gitRun(t, "-C", filepath.Join(fixture.repository, ".worktrees", "offline"), "rev-parse", "HEAD^{commit}"); commit != fixture.staleCommit {
		t.Fatalf("--no-fetch Task Worktree commit = %s, want current remote-tracking commit %s", commit, fixture.staleCommit)
	}

	disabled := false
	updateConfiguration(t, environment, func(configuration *config.Config) {
		repositoryConfiguration := configuration.Repositories["service"]
		repositoryConfiguration.Fetch = &disabled
		configuration.Repositories["service"] = repositoryConfiguration
	})
	if result := environment.run(t, "new", "configured-offline"); result.code != 0 {
		t.Fatalf("new configured-offline failed: %s", result.stderr)
	}
	configuredOffline := environment.run(t, "add", "configured-offline", "service")
	if configuredOffline.code != 0 {
		t.Fatalf("add with repository fetch disabled failed: code=%d stderr=%s", configuredOffline.code, configuredOffline.stderr)
	}
	if commit := gitRun(t, "-C", filepath.Join(fixture.repository, ".worktrees", "configured-offline"), "rev-parse", "HEAD^{commit}"); commit != fixture.staleCommit {
		t.Fatalf("repository fetch=false commit = %s, want stale commit %s", commit, fixture.staleCommit)
	}
	if result := environment.run(t, "new", "online"); result.code != 0 {
		t.Fatalf("new online failed: %s", result.stderr)
	}
	online := environment.run(t, "add", "online", "service", "--fetch")
	if online.code != 0 {
		t.Fatalf("add --fetch failed: code=%d stderr=%s", online.code, online.stderr)
	}
	if commit := gitRun(t, "-C", filepath.Join(fixture.repository, ".worktrees", "online"), "rev-parse", "HEAD^{commit}"); commit != fixture.freshCommit {
		t.Fatalf("--fetch Task Worktree commit = %s, want fetched commit %s", commit, fixture.freshCommit)
	}
}

func TestAddRejectsConflictingFetchFlagsAsValidationError(t *testing.T) {
	environment := initializedCLIEnvironment(t)

	result := environment.run(t, "add", "billing", "service", "--fetch", "--no-fetch")

	if result.code != 2 || !strings.Contains(result.stderr, "mutually exclusive") {
		t.Fatalf("conflicting fetch flags result: code=%d stderr=%q", result.code, result.stderr)
	}
}

func TestAddAttachesExistingUnassignedTaskBranchWithoutApplyingBase(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	repository := filepath.Join(t.TempDir(), "service")
	gitRun(t, "init", "-b", "main", repository)
	gitRun(t, "-C", repository, "config", "user.name", "Test")
	gitRun(t, "-C", repository, "config", "user.email", "test@example.com")
	tracked := filepath.Join(repository, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("initial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, "-C", repository, "add", "tracked.txt")
	gitRun(t, "-C", repository, "commit", "-m", "initial")
	gitRun(t, "-C", repository, "branch", "feat/billing")
	existingTip := gitRun(t, "-C", repository, "rev-parse", "refs/heads/feat/billing^{commit}")
	if result := environment.run(t, "repo", "add", "service", repository); result.code != 0 {
		t.Fatalf("repo add failed: %s", result.stderr)
	}
	if result := environment.run(t, "new", "billing"); result.code != 0 {
		t.Fatalf("new failed: %s", result.stderr)
	}

	result := environment.run(t, "add", "billing", "service", "--base", "bad branch")

	if result.code != 0 {
		t.Fatalf("add existing Task Branch failed: code=%d stderr=%s", result.code, result.stderr)
	}
	if !strings.Contains(result.stdout, "existing Task Branch") || !strings.Contains(result.stdout, "Base Ref was not applied") {
		t.Fatalf("stdout = %q, want existing-branch Base Ref notice", result.stdout)
	}
	worktree := filepath.Join(repository, ".worktrees", "billing")
	if branch := gitRun(t, "-C", worktree, "branch", "--show-current"); branch != "feat/billing" {
		t.Fatalf("Task Worktree branch = %q, want feat/billing", branch)
	}
	if commit := gitRun(t, "-C", worktree, "rev-parse", "HEAD^{commit}"); commit != existingTip {
		t.Fatalf("existing Task Branch moved to %s, want unchanged %s", commit, existingTip)
	}
	metadata := readPersistedTask(t, environment, "billing")
	if len(metadata.Attachments) != 1 || !metadata.Attachments[0].BranchExisted {
		t.Fatalf("Repository Attachment = %#v, want branch_existed", metadata.Attachments)
	}
	if metadata.Attachments[0].BaseRef != "" || metadata.Attachments[0].BaseCommit != "" {
		t.Fatalf("existing branch recorded unapplied Base Ref: %#v", metadata.Attachments[0])
	}
}

func TestAddRejectsMissingRemoteAndLocalBaseWithoutMutation(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	repository := createCommittedRepository(t, "service")
	if result := environment.run(t, "repo", "add", "service", repository); result.code != 0 {
		t.Fatalf("repo add failed: %s", result.stderr)
	}
	updateConfiguration(t, environment, func(configuration *config.Config) {
		configuration.Defaults.Remote = ""
	})
	if result := environment.run(t, "new", "billing"); result.code != 0 {
		t.Fatalf("new failed: %s", result.stderr)
	}

	result := environment.run(t, "add", "billing", "service", "--base", "missing")

	if result.code != 2 || !strings.Contains(result.stderr, "Base Ref \"missing\"") || !strings.Contains(result.stderr, "does not exist") {
		t.Fatalf("missing Base Ref result: code=%d stderr=%q", result.code, result.stderr)
	}
	if branch := gitRun(t, "-C", repository, "branch", "--list", "feat/billing"); branch != "" {
		t.Fatalf("missing Base Ref created Task Branch %q", branch)
	}
	if metadata := readPersistedTask(t, environment, "billing"); len(metadata.Attachments) != 0 {
		t.Fatalf("missing Base Ref persisted attachments: %#v", metadata.Attachments)
	}
}

func TestAddCompletesCollisionPreflightBeforeFetch(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	fixture := createAdvancedRemoteRepository(t)
	if result := environment.run(t, "repo", "add", "service", fixture.repository); result.code != 0 {
		t.Fatalf("repo add failed: %s", result.stderr)
	}
	if result := environment.run(t, "new", "billing"); result.code != 0 {
		t.Fatalf("new failed: %s", result.stderr)
	}
	collision := filepath.Join(environment.dataHome, "devtask", "workspaces", "billing", "service")
	if err := os.WriteFile(collision, []byte("user owned\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := environment.run(t, "add", "billing", "service")

	if result.code != 2 || !strings.Contains(result.stderr, "Task Workspace collision") {
		t.Fatalf("collision result: code=%d stderr=%q", result.code, result.stderr)
	}
	if commit := gitRun(t, "-C", fixture.repository, "rev-parse", "refs/remotes/origin/main^{commit}"); commit != fixture.staleCommit {
		t.Fatalf("collision fetched remote-tracking Base Ref to %s, want unchanged %s", commit, fixture.staleCommit)
	}
}

func TestAddAbortsWhenConfiguredRemoteFetchFails(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	repository := createCommittedRepository(t, "service")
	missingRemote := filepath.Join(t.TempDir(), "missing.git")
	gitRun(t, "-C", repository, "remote", "add", "origin", missingRemote)
	if result := environment.run(t, "repo", "add", "service", repository); result.code != 0 {
		t.Fatalf("repo add failed: %s", result.stderr)
	}
	if result := environment.run(t, "new", "billing"); result.code != 0 {
		t.Fatalf("new failed: %s", result.stderr)
	}

	result := environment.run(t, "add", "billing", "service")

	if result.code != 1 || !strings.Contains(result.stderr, "fetch configured remote \"origin\"") {
		t.Fatalf("fetch failure result: code=%d stderr=%q", result.code, result.stderr)
	}
	if branch := gitRun(t, "-C", repository, "branch", "--list", "feat/billing"); branch != "" {
		t.Fatalf("fetch failure created Task Branch %q", branch)
	}
	if _, err := os.Lstat(filepath.Join(repository, ".worktrees", "billing")); !os.IsNotExist(err) {
		t.Fatalf("fetch failure created Task Worktree: %v", err)
	}
	if metadata := readPersistedTask(t, environment, "billing"); len(metadata.Attachments) != 0 {
		t.Fatalf("fetch failure persisted attachments: %#v", metadata.Attachments)
	}
}

func TestAddResolvesBaseBranchInCLIRepositoryGlobalOrder(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	repository := createCommittedRepository(t, "service")
	branchCommits := map[string]string{}
	for _, branch := range []string{"global-base", "repository-base", "cli-base"} {
		gitRun(t, "-C", repository, "checkout", "-b", branch, "main")
		path := filepath.Join(repository, branch+".txt")
		if err := os.WriteFile(path, []byte(branch+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitRun(t, "-C", repository, "add", filepath.Base(path))
		gitRun(t, "-C", repository, "commit", "-m", branch)
		branchCommits[branch] = gitRun(t, "-C", repository, "rev-parse", "HEAD^{commit}")
		gitRun(t, "-C", repository, "checkout", "main")
	}
	if result := environment.run(t, "repo", "add", "service", repository); result.code != 0 {
		t.Fatalf("repo add failed: %s", result.stderr)
	}
	updateConfiguration(t, environment, func(configuration *config.Config) {
		configuration.Defaults.BaseBranch = "global-base"
		repositoryConfiguration := configuration.Repositories["service"]
		repositoryConfiguration.BaseBranch = "repository-base"
		configuration.Repositories["service"] = repositoryConfiguration
	})

	for _, taskName := range []string{"repository-setting", "cli-setting"} {
		if result := environment.run(t, "new", taskName); result.code != 0 {
			t.Fatalf("new %s failed: %s", taskName, result.stderr)
		}
	}
	if result := environment.run(t, "add", "repository-setting", "service", "--no-fetch"); result.code != 0 {
		t.Fatalf("repository Base Ref add failed: %s", result.stderr)
	}
	if result := environment.run(t, "add", "cli-setting", "service", "--base", "cli-base", "--no-fetch"); result.code != 0 {
		t.Fatalf("CLI Base Ref add failed: %s", result.stderr)
	}
	assertAttachmentBase(t, environment, "repository-setting", "repository-base", "refs/heads/repository-base", branchCommits["repository-base"])
	assertAttachmentBase(t, environment, "cli-setting", "cli-base", "refs/heads/cli-base", branchCommits["cli-base"])

	updateConfiguration(t, environment, func(configuration *config.Config) {
		repositoryConfiguration := configuration.Repositories["service"]
		repositoryConfiguration.BaseBranch = ""
		configuration.Repositories["service"] = repositoryConfiguration
	})
	if result := environment.run(t, "new", "global-setting"); result.code != 0 {
		t.Fatalf("new global-setting failed: %s", result.stderr)
	}
	if result := environment.run(t, "add", "global-setting", "service", "--no-fetch"); result.code != 0 {
		t.Fatalf("global Base Ref add failed: %s", result.stderr)
	}
	assertAttachmentBase(t, environment, "global-setting", "global-base", "refs/heads/global-base", branchCommits["global-base"])
}

func TestAddFallsBackToLocalBaseWhenRemoteBranchIsAbsent(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	remote := filepath.Join(t.TempDir(), "service.git")
	gitRun(t, "init", "--bare", remote)
	repository := createCommittedRepository(t, "service")
	gitRun(t, "-C", repository, "remote", "add", "origin", remote)
	gitRun(t, "-C", repository, "push", "origin", "main")
	gitRun(t, "-C", repository, "branch", "local-only")
	localCommit := gitRun(t, "-C", repository, "rev-parse", "refs/heads/local-only^{commit}")
	if result := environment.run(t, "repo", "add", "service", repository); result.code != 0 {
		t.Fatalf("repo add failed: %s", result.stderr)
	}
	if result := environment.run(t, "new", "billing"); result.code != 0 {
		t.Fatalf("new failed: %s", result.stderr)
	}

	result := environment.run(t, "add", "billing", "service", "--base", "local-only")

	if result.code != 0 {
		t.Fatalf("local Base Ref fallback failed: code=%d stderr=%s", result.code, result.stderr)
	}
	assertAttachmentBase(t, environment, "billing", "local-only", "refs/heads/local-only", localCommit)
}

func TestAddRefusesTaskBranchOwnedByLiveOrPrunableWorktree(t *testing.T) {
	for _, test := range []struct {
		name     string
		prunable bool
	}{
		{name: "live"},
		{name: "prunable", prunable: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			environment := initializedCLIEnvironment(t)
			repository := createCommittedRepository(t, "service")
			owner := filepath.Join(t.TempDir(), "branch-owner")
			gitRun(t, "-C", repository, "worktree", "add", "-b", "feat/billing", owner, "main")
			if test.prunable {
				if err := os.RemoveAll(owner); err != nil {
					t.Fatal(err)
				}
			}
			if result := environment.run(t, "repo", "add", "service", repository); result.code != 0 {
				t.Fatalf("repo add failed: %s", result.stderr)
			}
			if result := environment.run(t, "new", "billing"); result.code != 0 {
				t.Fatalf("new failed: %s", result.stderr)
			}

			result := environment.run(t, "add", "billing", "service")

			if result.code != 2 || !strings.Contains(result.stderr, owner) || !strings.Contains(result.stderr, "refusing to prune or steal") {
				t.Fatalf("occupied Task Branch result: code=%d stderr=%q", result.code, result.stderr)
			}
			if test.prunable && !strings.Contains(result.stderr, "prunable") {
				t.Fatalf("stale Task Branch result did not identify prunable record: %q", result.stderr)
			}
			if _, err := os.Lstat(filepath.Join(repository, ".worktrees", "billing")); !os.IsNotExist(err) {
				t.Fatalf("occupied Task Branch created expected Task Worktree: %v", err)
			}
			if metadata := readPersistedTask(t, environment, "billing"); len(metadata.Attachments) != 0 {
				t.Fatalf("occupied Task Branch persisted attachments: %#v", metadata.Attachments)
			}
		})
	}
}

func TestAddCanonicalizesRepositorySymlinkAndRejectsWorktreeRootSymlink(t *testing.T) {
	t.Run("Registered Repository symlink", func(t *testing.T) {
		environment := initializedCLIEnvironment(t)
		repository := createCommittedRepository(t, "service")
		aliasPath := filepath.Join(t.TempDir(), "service-alias")
		if err := os.Symlink(repository, aliasPath); err != nil {
			t.Fatal(err)
		}
		canonicalRepository, err := filepath.EvalSymlinks(repository)
		if err != nil {
			t.Fatal(err)
		}
		if result := environment.run(t, "repo", "add", "service", aliasPath); result.code != 0 {
			t.Fatalf("repo add through symlink failed: %s", result.stderr)
		}
		if result := environment.run(t, "new", "billing"); result.code != 0 {
			t.Fatalf("new failed: %s", result.stderr)
		}

		result := environment.run(t, "add", "billing", "service", "--no-fetch")

		if result.code != 0 {
			t.Fatalf("add through repository symlink failed: code=%d stderr=%s", result.code, result.stderr)
		}
		metadata := readPersistedTask(t, environment, "billing")
		if len(metadata.Attachments) != 1 || metadata.Attachments[0].MainCheckout != canonicalRepository {
			t.Fatalf("Repository Attachment = %#v, want canonical Main Checkout %q", metadata.Attachments, canonicalRepository)
		}
		wantWorktree := filepath.Join(canonicalRepository, ".worktrees", "billing")
		if metadata.Attachments[0].WorktreePath != wantWorktree {
			t.Fatalf("Task Worktree path = %q, want %q", metadata.Attachments[0].WorktreePath, wantWorktree)
		}
	})

	t.Run("worktrees root symlink", func(t *testing.T) {
		environment := initializedCLIEnvironment(t)
		repository := createCommittedRepository(t, "service")
		escape := filepath.Join(t.TempDir(), "outside")
		if err := os.Mkdir(escape, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(escape, filepath.Join(repository, ".worktrees")); err != nil {
			t.Fatal(err)
		}
		if result := environment.run(t, "repo", "add", "service", repository); result.code != 0 {
			t.Fatalf("repo add failed: %s", result.stderr)
		}
		if result := environment.run(t, "new", "billing"); result.code != 0 {
			t.Fatalf("new failed: %s", result.stderr)
		}

		result := environment.run(t, "add", "billing", "service", "--no-fetch")

		if result.code != 2 || !strings.Contains(result.stderr, "worktrees path") || !strings.Contains(result.stderr, "real directory") {
			t.Fatalf("escaped worktrees root result: code=%d stderr=%q", result.code, result.stderr)
		}
		if entries, err := os.ReadDir(escape); err != nil || len(entries) != 0 {
			t.Fatalf("escaped directory changed: entries=%v error=%v", entries, err)
		}
		if branch := gitRun(t, "-C", repository, "branch", "--list", "feat/billing"); branch != "" {
			t.Fatalf("escaped worktrees root created Task Branch %q", branch)
		}
	})
}

func createCommittedRepository(t *testing.T, name string) string {
	t.Helper()
	repository := filepath.Join(t.TempDir(), name)
	gitRun(t, "init", "-b", "main", repository)
	gitRun(t, "-C", repository, "config", "user.name", "Test")
	gitRun(t, "-C", repository, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(repository, "tracked.txt"), []byte("initial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, "-C", repository, "add", "tracked.txt")
	gitRun(t, "-C", repository, "commit", "-m", "initial")
	return repository
}

type advancedRemoteRepository struct {
	repository  string
	staleCommit string
	freshCommit string
}

func createAdvancedRemoteRepository(t *testing.T) advancedRemoteRepository {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "service.git")
	gitRun(t, "init", "--bare", remote)
	seed := createCommittedRepository(t, "seed")
	tracked := filepath.Join(seed, "tracked.txt")
	gitRun(t, "-C", seed, "remote", "add", "origin", remote)
	gitRun(t, "-C", seed, "push", "-u", "origin", "main")
	repository := filepath.Join(t.TempDir(), "service")
	gitRun(t, "clone", "--branch", "main", remote, repository)
	staleCommit := gitRun(t, "-C", repository, "rev-parse", "refs/remotes/origin/main^{commit}")
	if err := os.WriteFile(tracked, []byte("fresh remote\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, "-C", seed, "add", "tracked.txt")
	gitRun(t, "-C", seed, "commit", "-m", "advance remote")
	gitRun(t, "-C", seed, "push", "origin", "main")
	freshCommit := gitRun(t, "-C", seed, "rev-parse", "HEAD^{commit}")
	if freshCommit == staleCommit {
		t.Fatal("test remote did not advance")
	}
	return advancedRemoteRepository{repository: repository, staleCommit: staleCommit, freshCommit: freshCommit}
}

func updateConfiguration(t *testing.T, environment cliTestEnvironment, update func(*config.Config)) {
	t.Helper()
	path := filepath.Join(environment.configHome, "devtask", "config.yaml")
	configuration, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	update(&configuration)
	contents, err := yaml.Marshal(configuration)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertAttachmentBase(t *testing.T, environment cliTestEnvironment, taskName, branch, ref, commit string) {
	t.Helper()
	metadata := readPersistedTask(t, environment, taskName)
	if len(metadata.Attachments) != 1 {
		t.Fatalf("%s attachments = %#v, want one", taskName, metadata.Attachments)
	}
	attachment := metadata.Attachments[0]
	if attachment.BaseBranch != branch || attachment.BaseRef != ref || attachment.BaseCommit != commit {
		t.Fatalf("%s Base Ref snapshot = %#v, want %s at %s", taskName, attachment, ref, commit)
	}
}
