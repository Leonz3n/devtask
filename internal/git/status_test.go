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
