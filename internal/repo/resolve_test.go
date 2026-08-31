package repo

import "testing"

func TestFirstMainCheckoutParsesNULTerminatedPathVerbatim(t *testing.T) {
	want := "/tmp/service\nwith-unicode-\u00e9"
	output := []byte("worktree " + want + "\x00HEAD abc123\x00\x00")

	got, err := firstMainCheckout(output)

	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("firstMainCheckout() = %q, want %q", got, want)
	}
}

func TestFirstMainCheckoutRejectsMalformedPorcelain(t *testing.T) {
	for _, output := range [][]byte{nil, []byte("worktree \x00"), []byte("worktree /tmp/service\n")} {
		if _, err := firstMainCheckout(output); err == nil {
			t.Fatalf("firstMainCheckout(%q) succeeded, want error", output)
		}
	}
}
