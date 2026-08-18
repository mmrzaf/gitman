package apperr

import (
	"errors"
	"testing"
)

func TestWrapPreservesKindAndCause(t *testing.T) {
	cause := errors.New("db down")
	err := Wrap(KindUnavailable, "temporarily unavailable", cause)
	if KindOf(err) != KindUnavailable {
		t.Fatalf("kind = %v", KindOf(err))
	}
	if !errors.Is(err, cause) {
		t.Fatal("wrapped cause was lost")
	}
	if PublicMessage(err) != "temporarily unavailable" {
		t.Fatalf("public = %q", PublicMessage(err))
	}
}

func TestWrapNilCauseStillReturnsApplicationError(t *testing.T) {
	err := Wrap(KindUnavailable, "unavailable", nil)
	if err == nil {
		t.Fatal("Wrap with nil cause must not erase the application failure")
	}
	if KindOf(err) != KindUnavailable || PublicMessage(err) != "unavailable" {
		t.Fatalf("unexpected wrapped nil-cause error: kind=%v public=%q", KindOf(err), PublicMessage(err))
	}
}
