package secret

import "testing"

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	b, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []string{"", "plex-token-xyz", "é🔑"} {
		got, err := b.Open(b.Seal(in))
		if err != nil || got != in {
			t.Fatalf("round trip %q: got %q, %v", in, got, err)
		}
	}
	// The key persists: a second Load opens what the first sealed.
	sealed := b.Seal("persist")
	b2, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := b2.Open(sealed); err != nil || got != "persist" {
		t.Fatalf("reload: got %q, %v", got, err)
	}
	if _, err := b2.Open("bm90IHNlYWxlZA=="); err == nil {
		t.Fatal("tampered value opened")
	}
}
