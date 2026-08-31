package git

import "testing"

func TestParseStatusPorcelainV1ZConsumesRenameAndUnusualFilenames(t *testing.T) {
	status, err := ParseStatusPorcelainV1Z([]byte(" M tracked.txt\x00A  staged.txt\x00R  renamed\nfile.txt\x00rename-source.txt\x00?? untracked\n\tfile.txt\x00"))
	if err != nil {
		t.Fatal(err)
	}
	if !status.Modified || !status.Staged || !status.Untracked || status.Conflicted {
		t.Fatalf("parsed status = %#v", status)
	}
}

func TestParseStatusPorcelainV1ZRecognizesEveryUnmergedForm(t *testing.T) {
	status, err := ParseStatusPorcelainV1Z([]byte("DD both-deleted\x00AU added-by-us\x00UD deleted-by-them\x00UA added-by-them\x00DU deleted-by-us\x00AA both-added\x00UU both-modified\x00"))
	if err != nil {
		t.Fatal(err)
	}
	if !status.Conflicted {
		t.Fatalf("parsed status = %#v, want conflicted", status)
	}
}

func TestParseStatusPorcelainV1ZRejectsMalformedRecords(t *testing.T) {
	tests := [][]byte{
		[]byte(" M not-terminated"),
		[]byte("bad\x00"),
		[]byte("R  renamed\x00"),
	}
	for _, input := range tests {
		if _, err := ParseStatusPorcelainV1Z(input); err == nil {
			t.Fatalf("ParseStatusPorcelainV1Z(%q) succeeded", input)
		}
	}
}

func TestParseNULPathsPreservesUnusualIgnoredNames(t *testing.T) {
	paths, err := parseNULPaths([]byte("local.cache\x00nested/name\nwith-tab\t\x00"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[0] != "local.cache" || paths[1] != "nested/name\nwith-tab\t" {
		t.Fatalf("ignored paths = %#v", paths)
	}
	if empty, err := parseNULPaths(nil); err != nil || len(empty) != 0 {
		t.Fatalf("empty ignored paths = %#v, %v", empty, err)
	}
}

func TestParseNULPathsRejectsUnterminatedOutput(t *testing.T) {
	if _, err := parseNULPaths([]byte("local.cache")); err == nil {
		t.Fatal("unterminated ignored-path output succeeded")
	}
}
