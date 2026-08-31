package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPathWithinUsesPathComponentsRatherThanStringPrefixes(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "work", "service")
	for _, test := range []struct {
		name string
		path string
		want bool
	}{
		{name: "root", path: root, want: true},
		{name: "child", path: filepath.Join(root, "local"), want: true},
		{name: "sibling prefix", path: root + "-other", want: false},
		{name: "parent", path: filepath.Dir(root), want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := pathWithin(root, test.path)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("pathWithin(%q, %q) = %t, want %t", root, test.path, got, test.want)
			}
		})
	}
}

func TestEntryContainmentAllowsOnlyTheFinalSymlinkToEscape(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repository")
	outside := t.TempDir()
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	finalLink := filepath.Join(root, "local-link")
	if err := os.Symlink(filepath.Join(outside, "missing"), finalLink); err != nil {
		t.Fatal(err)
	}
	allowed, err := entryRemainsWithin(canonicalRoot, finalLink, true)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Fatal("final Shared Local Path symlink was treated as escaping")
	}

	escapingParent := filepath.Join(root, "escape")
	if err := os.Symlink(outside, escapingParent); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	escaped, err := entryRemainsWithin(canonicalRoot, filepath.Join(escapingParent, "secret"), false)
	if err != nil {
		t.Fatal(err)
	}
	if escaped {
		t.Fatal("Shared Local Path through an escaping parent symlink was accepted")
	}
}
