package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemovalProjectionRemovesExactLinkAndGeneratedEntry(t *testing.T) {
	workspacePath := t.TempDir()
	worktreePath := filepath.Join(t.TempDir(), "worktree")
	if err := os.Mkdir(worktreePath, 0o700); err != nil {
		t.Fatal(err)
	}
	canonicalWorkspace, err := filepath.EvalSymlinks(workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	target, err := filepath.Rel(canonicalWorkspace, worktreePath)
	if err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(workspacePath, "invoice")
	if err := os.Symlink(target, linkPath); err != nil {
		t.Fatal(err)
	}
	agentsPath := filepath.Join(workspacePath, "AGENTS.md")
	agents := append([]byte("# Manual\n\n"), generatedSection("billing", "feat/billing", []Attachment{{Alias: "invoice", WorktreePath: worktreePath}})...)
	if err := os.WriteFile(agentsPath, agents, 0o600); err != nil {
		t.Fatal(err)
	}

	projection, err := PrepareRemovalProjection(workspacePath, "billing", "feat/billing", Attachment{Alias: "invoice", WorktreePath: worktreePath}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := projection.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(linkPath); !os.IsNotExist(err) {
		t.Fatalf("removed projection link remains: %v", err)
	}
	updated, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(updated), "# Manual\n\n") || !strings.Contains(string(updated), "None.") || strings.Contains(string(updated), "`invoice`") {
		t.Fatalf("updated AGENTS.md = %q", updated)
	}
}

func TestRemovalProjectionRejectsAliasOutsideWorkspace(t *testing.T) {
	workspacePath := t.TempDir()
	agentsPath := filepath.Join(workspacePath, "AGENTS.md")
	if err := os.WriteFile(agentsPath, generatedSection("billing", "feat/billing", nil), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(workspacePath), "outside-link")
	if err := os.Symlink("target", outside); err != nil {
		t.Fatal(err)
	}

	if _, err := PrepareRemovalProjection(workspacePath, "billing", "feat/billing", Attachment{Alias: "../outside-link", WorktreePath: "target"}, nil, false); err == nil {
		t.Fatal("escaping Repository Alias was accepted")
	}
	if _, err := os.Lstat(outside); err != nil {
		t.Fatalf("escaping link was changed: %v", err)
	}
}
