package token

import "testing"

func TestNewIsRandomAndURLSafe(t *testing.T) {
	a, err := New(32)
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(32)
	if err != nil {
		t.Fatal(err)
	}
	if a == b || len(a) != 43 {
		t.Fatalf("New(32) = %q, %q", a, b)
	}
}

func TestHashIsStableAndDistinct(t *testing.T) {
	first, second := Hash("a"), Hash("a")
	if first != second || first == Hash("b") || len(first) != 64 {
		t.Fatal("Hash is not a stable, distinct SHA-256 hex digest")
	}
}
