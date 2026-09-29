package store

import (
	"context"
	"testing"
)

func newTest(t *testing.T) *Store {
	t.Helper()
	s, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSettings(t *testing.T) {
	s, ctx := newTest(t), context.Background()
	if v, err := s.Setting(ctx, "missing"); err != nil || v != "" {
		t.Fatalf("missing: %q, %v", v, err)
	}
	for _, v := range []string{"a", "b"} {
		if err := s.SetSetting(ctx, "k", v); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.Setting(ctx, "k"); got != v {
			t.Fatalf("got %q, want %q", got, v)
		}
	}
}
