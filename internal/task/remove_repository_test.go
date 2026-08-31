package task

import (
	"testing"

	"github.com/Leonz3n/devtask/internal/config"
)

func TestRemovalBaseSettingsUseAttachmentBranchAndCurrentRepositoryOverrides(t *testing.T) {
	configuredFetch := false
	configuration := config.Default()
	configuration.Repositories["Invoice"] = config.RepositoryConfig{
		BaseBranch: "ignored-current-setting",
		Remote:     "upstream",
		Fetch:      &configuredFetch,
	}
	attachment := RepositoryAttachment{Alias: "invoice", BaseBranch: "snapshotted-base"}
	forcedFetch := true

	branch, remote, fetch, err := removalBaseSettings(configuration, attachment, &forcedFetch)

	if err != nil {
		t.Fatal(err)
	}
	if branch != "snapshotted-base" || remote != "upstream" || !fetch {
		t.Fatalf("removal settings = %q, %q, %v", branch, remote, fetch)
	}
}

func TestRemovalBaseSettingsFallBackToGlobalBranch(t *testing.T) {
	configuration := config.Default()
	configuration.Defaults.BaseBranch = "trunk"
	configuration.Defaults.Remote = ""
	configuration.Defaults.Fetch = false

	branch, remote, fetch, err := removalBaseSettings(configuration, RepositoryAttachment{Alias: "invoice"}, nil)

	if err != nil {
		t.Fatal(err)
	}
	if branch != "trunk" || remote != "" || fetch {
		t.Fatalf("fallback removal settings = %q, %q, %v", branch, remote, fetch)
	}
}
