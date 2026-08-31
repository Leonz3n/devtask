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

func TestAddProjectsIgnoredSharedLocalPathKindsAsOwnedRelativeLinks(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	repository := createCommittedRepository(t, "service")
	ignore := ".env\ncerts\nlocal-link\n"
	if err := os.WriteFile(filepath.Join(repository, ".gitignore"), []byte(ignore), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, "-C", repository, "add", ".gitignore")
	gitRun(t, "-C", repository, "commit", "-m", "ignore local settings")

	sources := []string{".env", "certs", "local-link"}
	if err := os.WriteFile(filepath.Join(repository, ".env"), []byte("TOKEN=local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repository, "certs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "certs", "client.pem"), []byte("certificate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	missingOutsideTarget := filepath.Join(t.TempDir(), "missing-target")
	if err := os.Symlink(missingOutsideTarget, filepath.Join(repository, "local-link")); err != nil {
		t.Fatal(err)
	}

	if result := environment.run(t, "repo", "add", "service", repository); result.code != 0 {
		t.Fatalf("repo add failed: %s", result.stderr)
	}
	canonicalRepository, err := filepath.EvalSymlinks(repository)
	if err != nil {
		t.Fatal(err)
	}
	updateConfiguration(t, environment, func(configuration *config.Config) {
		repositoryConfiguration := configuration.Repositories["service"]
		repositoryConfiguration.SharedPaths = sources
		configuration.Repositories["service"] = repositoryConfiguration
	})
	if result := environment.run(t, "new", "billing"); result.code != 0 {
		t.Fatalf("new failed: %s", result.stderr)
	}

	result := environment.run(t, "add", "billing", "service", "--no-fetch")

	if result.code != 0 || result.stderr != "" {
		t.Fatalf("add result: code=%d stderr=%q", result.code, result.stderr)
	}
	worktree := filepath.Join(canonicalRepository, ".worktrees", "billing")
	wantLinks := make([]persistedManagedLink, 0, len(sources))
	linkInfos := make(map[string]os.FileInfo, len(sources))
	for _, sharedPath := range sources {
		source := filepath.Join(canonicalRepository, sharedPath)
		destination := filepath.Join(worktree, sharedPath)
		wantTarget, err := filepath.Rel(filepath.Dir(destination), source)
		if err != nil {
			t.Fatal(err)
		}
		gotTarget, err := os.Readlink(destination)
		if err != nil {
			t.Fatalf("read projected Shared Local Path %q: %v", sharedPath, err)
		}
		if gotTarget != wantTarget {
			t.Fatalf("Shared Local Path %q target = %q, want relative target %q", sharedPath, gotTarget, wantTarget)
		}
		linkInfos[sharedPath], err = os.Lstat(destination)
		if err != nil {
			t.Fatal(err)
		}
		wantLinks = append(wantLinks, persistedManagedLink{Source: source, Destination: destination, Target: wantTarget})
	}
	if status := gitRun(t, "-C", worktree, "status", "--porcelain"); status != "" {
		t.Fatalf("projected Shared Local Paths dirtied Task Worktree: %q", status)
	}
	metadata := readPersistedTask(t, environment, "billing")
	if len(metadata.Attachments) != 1 || !reflect.DeepEqual(metadata.Attachments[0].ManagedLinks, wantLinks) {
		t.Fatalf("managed links = %#v, want %#v", metadata.Attachments, wantLinks)
	}

	repeated := environment.run(t, "add", "billing", "service", "--no-fetch")

	if repeated.code != 0 || repeated.stderr != "" {
		t.Fatalf("repeated add result: code=%d stderr=%q", repeated.code, repeated.stderr)
	}
	for _, sharedPath := range sources {
		current, err := os.Lstat(filepath.Join(worktree, sharedPath))
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(linkInfos[sharedPath], current) {
			t.Fatalf("repeated add replaced Shared Local Path link %q", sharedPath)
		}
	}
	repeatedMetadata := readPersistedTask(t, environment, "billing")
	if !reflect.DeepEqual(repeatedMetadata.Attachments[0].ManagedLinks, wantLinks) {
		t.Fatalf("repeated add changed managed links: %#v", repeatedMetadata.Attachments[0].ManagedLinks)
	}
}

func TestAddRecordsChangedSharedLocalPathIdentityWhenCompensationIsIncomplete(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	repository := createCommittedRepository(t, "service")
	if err := os.WriteFile(filepath.Join(repository, ".gitignore"), []byte(".env\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, "-C", repository, "add", ".gitignore")
	gitRun(t, "-C", repository, "commit", "-m", "ignore local settings")
	if err := os.WriteFile(filepath.Join(repository, ".env"), []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if result := environment.run(t, "repo", "add", "service", repository); result.code != 0 {
		t.Fatalf("repo add failed: %s", result.stderr)
	}
	updateConfiguration(t, environment, func(configuration *config.Config) {
		repositoryConfiguration := configuration.Repositories["service"]
		repositoryConfiguration.SharedPaths = []string{".env"}
		configuration.Repositories["service"] = repositoryConfiguration
	})
	if result := environment.run(t, "new", "billing"); result.code != 0 {
		t.Fatalf("new failed: %s", result.stderr)
	}
	canonicalRepository, err := filepath.EvalSymlinks(repository)
	if err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(canonicalRepository, ".worktrees", "billing")
	destination := filepath.Join(worktree, ".env")
	signalPath := filepath.Join(t.TempDir(), "projection")
	binary := devtaskBinaryWithTags(t, "devtask_test")
	command := exec.Command(binary, "add", "billing", "service", "--no-fetch")
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
			t.Fatal("add did not reach projection boundary")
		}
		time.Sleep(time.Millisecond)
	}
	expectedTarget, err := os.Readlink(destination)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(destination); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("user-replacement", destination); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(signalPath+".continue", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err = command.Wait()

	if err == nil {
		t.Fatal("fault-enabled add succeeded")
	}
	if exitError, ok := err.(*exec.ExitError); !ok || exitError.ExitCode() != 1 {
		t.Fatalf("add error = %v, want exit 1; stderr: %s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "refuse to remove changed Shared Local Path link") || !strings.Contains(stderr.String(), "incomplete") {
		t.Fatalf("stderr = %q, want incomplete managed-link diagnostic", stderr.String())
	}
	if target, err := os.Readlink(destination); err != nil || target != "user-replacement" {
		t.Fatalf("compensation changed replacement: target=%q error=%v", target, err)
	}
	metadata := readPersistedTask(t, environment, "billing")
	wantLink := persistedManagedLink{
		Source:      filepath.Join(canonicalRepository, ".env"),
		Destination: destination,
		Target:      expectedTarget,
	}
	if metadata.State != "incomplete" || len(metadata.Attachments) != 1 || !reflect.DeepEqual(metadata.Attachments[0].ManagedLinks, []persistedManagedLink{wantLink}) {
		t.Fatalf("Incomplete Attachment managed links = %#v, want %#v", metadata.Attachments, wantLink)
	}
}

func TestAddRollsBackSharedLocalPathsAcrossRepositoryBatch(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	repositories := map[string]string{
		"first":  createCommittedRepository(t, "first"),
		"second": createCommittedRepository(t, "second"),
	}
	for alias, repository := range repositories {
		if err := os.WriteFile(filepath.Join(repository, ".gitignore"), []byte(".env\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitRun(t, "-C", repository, "add", ".gitignore")
		gitRun(t, "-C", repository, "commit", "-m", "ignore local settings")
		if err := os.WriteFile(filepath.Join(repository, ".env"), []byte(alias+" secret\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if result := environment.run(t, "repo", "add", alias, repository); result.code != 0 {
			t.Fatalf("repo add %s failed: %s", alias, result.stderr)
		}
	}
	updateConfiguration(t, environment, func(configuration *config.Config) {
		for alias, repositoryConfiguration := range configuration.Repositories {
			repositoryConfiguration.SharedPaths = []string{".env"}
			configuration.Repositories[alias] = repositoryConfiguration
		}
	})
	if result := environment.run(t, "new", "billing"); result.code != 0 {
		t.Fatalf("new failed: %s", result.stderr)
	}

	binary := devtaskBinaryWithTags(t, "devtask_test")
	command := exec.Command(binary, "add", "billing", "first", "second", "--no-fetch")
	command.Dir = environment.home
	command.Env = append(filteredEnvironment(os.Environ()),
		"HOME="+environment.home,
		"XDG_CONFIG_HOME="+environment.configHome,
		"XDG_DATA_HOME="+environment.dataHome,
		"DEVTASK_TEST_FAIL_AFTER_PROJECTION=1",
	)
	output, err := command.CombinedOutput()

	if err == nil {
		t.Fatal("fault-enabled batch add succeeded")
	}
	if exitError, ok := err.(*exec.ExitError); !ok || exitError.ExitCode() != 1 {
		t.Fatalf("batch add error = %v, want exit 1; output: %s", err, output)
	}
	for alias, repository := range repositories {
		if contents, err := os.ReadFile(filepath.Join(repository, ".env")); err != nil || string(contents) != alias+" secret\n" {
			t.Fatalf("rollback changed %s source: contents=%q error=%v", alias, contents, err)
		}
		if _, err := os.Lstat(filepath.Join(repository, ".worktrees", "billing")); !os.IsNotExist(err) {
			t.Fatalf("rollback left %s Task Worktree: %v", alias, err)
		}
		if branch := gitRun(t, "-C", repository, "branch", "--list", "feat/billing"); branch != "" {
			t.Fatalf("rollback left %s Task Branch Name %q", alias, branch)
		}
	}
	metadata := readPersistedTask(t, environment, "billing")
	if metadata.State != "ready" || len(metadata.Attachments) != 0 {
		t.Fatalf("rollback changed Task metadata: %#v", metadata)
	}
}

func TestAddSkipsInvalidSharedLocalPathsWithWarnings(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	repository := createCommittedRepository(t, "service")
	if err := os.WriteFile(filepath.Join(repository, ".gitignore"), []byte("dir-only/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, "-C", repository, "add", ".gitignore")
	gitRun(t, "-C", repository, "commit", "-m", "add local ignore rules")
	gitRun(t, "-C", repository, "branch", "base-before-tracked")
	if err := os.WriteFile(filepath.Join(repository, "tracked-later"), []byte("tracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, "-C", repository, "add", "tracked-later")
	gitRun(t, "-C", repository, "commit", "-m", "add tracked source")
	if err := os.WriteFile(filepath.Join(repository, "visible-local"), []byte("visible\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repository, "dir-only"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repository, "escape")); err != nil {
		t.Fatal(err)
	}
	configured := []string{"missing", "tracked-later", "visible-local", "escape/secret", "dir-only"}
	if result := environment.run(t, "repo", "add", "service", repository); result.code != 0 {
		t.Fatalf("repo add failed: %s", result.stderr)
	}
	updateConfiguration(t, environment, func(configuration *config.Config) {
		repositoryConfiguration := configuration.Repositories["service"]
		repositoryConfiguration.SharedPaths = configured
		configuration.Repositories["service"] = repositoryConfiguration
	})
	if result := environment.run(t, "new", "billing"); result.code != 0 {
		t.Fatalf("new failed: %s", result.stderr)
	}

	result := environment.run(t, "add", "billing", "service", "--base", "base-before-tracked", "--no-fetch")

	if result.code != 0 {
		t.Fatalf("add failed: code=%d stderr=%q", result.code, result.stderr)
	}
	for _, warning := range []string{
		`Shared Local Path "missing" skipped: source does not exist`,
		`Shared Local Path "tracked-later" skipped: source is tracked`,
		`Shared Local Path "visible-local" skipped: source is not effectively ignored`,
		`Shared Local Path "escape/secret" skipped: source escapes the Main Checkout`,
		`Shared Local Path "dir-only" skipped: destination is not effectively ignored`,
	} {
		if !strings.Contains(result.stderr, warning) {
			t.Errorf("stderr = %q, want warning %q", result.stderr, warning)
		}
	}
	canonicalRepository, err := filepath.EvalSymlinks(repository)
	if err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(canonicalRepository, ".worktrees", "billing")
	for _, sharedPath := range configured {
		if _, err := os.Lstat(filepath.Join(worktree, sharedPath)); !os.IsNotExist(err) {
			t.Errorf("skipped Shared Local Path %q exists in Task Worktree: %v", sharedPath, err)
		}
	}
	metadata := readPersistedTask(t, environment, "billing")
	if len(metadata.Attachments) != 1 || len(metadata.Attachments[0].ManagedLinks) != 0 {
		t.Fatalf("skipped paths recorded as managed: %#v", metadata.Attachments)
	}
}

func TestAddPreservesEverySharedLocalPathDestinationCollision(t *testing.T) {
	environment := initializedCLIEnvironment(t)
	repository := createCommittedRepository(t, "service")
	configured := []string{"collision-file", "collision-dir", "collision-link", "collision-broken"}
	if err := os.WriteFile(filepath.Join(repository, ".gitignore"), []byte(strings.Join(configured, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, "-C", repository, "add", ".gitignore")
	gitRun(t, "-C", repository, "commit", "-m", "ignore local collisions")
	gitRun(t, "-C", repository, "checkout", "-b", "feat/billing")
	if err := os.WriteFile(filepath.Join(repository, "collision-file"), []byte("destination file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repository, "collision-dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "collision-dir", "kept"), []byte("destination directory\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("tracked.txt", filepath.Join(repository, "collision-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing-destination", filepath.Join(repository, "collision-broken")); err != nil {
		t.Fatal(err)
	}
	gitRun(t, "-C", repository, "add", "-f", "--", "collision-file", "collision-dir", "collision-link", "collision-broken")
	gitRun(t, "-C", repository, "commit", "-m", "add destination collisions")
	gitRun(t, "-C", repository, "checkout", "main")

	if err := os.WriteFile(filepath.Join(repository, "collision-file"), []byte("source file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repository, "collision-dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "collision-dir", "source"), []byte("source directory\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("source-link-target", filepath.Join(repository, "collision-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("source-broken-target", filepath.Join(repository, "collision-broken")); err != nil {
		t.Fatal(err)
	}
	if result := environment.run(t, "repo", "add", "service", repository); result.code != 0 {
		t.Fatalf("repo add failed: %s", result.stderr)
	}
	updateConfiguration(t, environment, func(configuration *config.Config) {
		repositoryConfiguration := configuration.Repositories["service"]
		repositoryConfiguration.SharedPaths = configured
		configuration.Repositories["service"] = repositoryConfiguration
	})
	if result := environment.run(t, "new", "billing"); result.code != 0 {
		t.Fatalf("new failed: %s", result.stderr)
	}

	result := environment.run(t, "add", "billing", "service", "--no-fetch")

	if result.code != 0 {
		t.Fatalf("add failed: code=%d stderr=%q", result.code, result.stderr)
	}
	for _, sharedPath := range configured {
		warning := `Shared Local Path "` + sharedPath + `" skipped: destination already exists`
		if !strings.Contains(result.stderr, warning) {
			t.Errorf("stderr = %q, want warning %q", result.stderr, warning)
		}
	}
	canonicalRepository, err := filepath.EvalSymlinks(repository)
	if err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(canonicalRepository, ".worktrees", "billing")
	if contents, err := os.ReadFile(filepath.Join(worktree, "collision-file")); err != nil || string(contents) != "destination file\n" {
		t.Fatalf("file collision changed: contents=%q error=%v", contents, err)
	}
	if contents, err := os.ReadFile(filepath.Join(worktree, "collision-dir", "kept")); err != nil || string(contents) != "destination directory\n" {
		t.Fatalf("directory collision changed: contents=%q error=%v", contents, err)
	}
	for path, wantTarget := range map[string]string{"collision-link": "tracked.txt", "collision-broken": "missing-destination"} {
		if target, err := os.Readlink(filepath.Join(worktree, path)); err != nil || target != wantTarget {
			t.Fatalf("symlink collision %q changed: target=%q error=%v", path, target, err)
		}
	}
	metadata := readPersistedTask(t, environment, "billing")
	if len(metadata.Attachments) != 1 || len(metadata.Attachments[0].ManagedLinks) != 0 {
		t.Fatalf("collisions recorded as managed: %#v", metadata.Attachments)
	}
}
