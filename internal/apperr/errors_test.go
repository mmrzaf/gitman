package apperr

import (
	"errors"
	"fmt"
	"testing"
)

func TestNewAndKindOf(t *testing.T) {
	err := New(KindNotFound, "repository not found")
	if KindOf(err) != KindNotFound {
		t.Errorf("KindOf = %v, want KindNotFound", KindOf(err))
	}
	if PublicMessage(err) != "repository not found" {
		t.Errorf("PublicMessage = %q", PublicMessage(err))
	}
}

func TestWrapPreservesUnderlyingError(t *testing.T) {
	underlying := errors.New("connection reset")
	err := Wrap(KindConflict, "database is temporarily unavailable", underlying)

	if KindOf(err) != KindConflict {
		t.Errorf("KindOf = %v, want KindConflict", KindOf(err))
	}
	if PublicMessage(err) != "database is temporarily unavailable" {
		t.Errorf("PublicMessage = %q", PublicMessage(err))
	}
	if !errors.Is(err, underlying) {
		t.Error("expected errors.Is to find the wrapped underlying error")
	}
}

func TestKindOfPlainError(t *testing.T) {
	err := fmt.Errorf("boom")
	if KindOf(err) != KindInternal {
		t.Errorf("KindOf(plain error) = %v, want KindInternal", KindOf(err))
	}
	if PublicMessage(err) == "boom" {
		t.Error("PublicMessage must not leak a plain error's text")
	}
}

func TestKindOfNil(t *testing.T) {
	if KindOf(nil) != KindInternal {
		t.Error("KindOf(nil) should report KindInternal")
	}
}
