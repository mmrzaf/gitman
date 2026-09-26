// Package apperr classifies errors by the response they deserve, so a
// database call, a Git operation or a validation failure can all be turned
// into the right HTTP status and the right message for the person who made
// the request, without every call site repeating that decision.
package apperr

import (
	"errors"
	"fmt"
)

// Kind classifies an error by the response it deserves, independent of the
// operation that produced it.
type Kind uint8

const (
	// KindInternal is anything no one classified: a failure in Gitman or
	// its host, never described to a person.
	KindInternal Kind = iota
	// KindInvalid is a request the person can correct.
	KindInvalid
	// KindForbidden is a request the person may not make.
	KindForbidden
	// KindNotFound names something that does not exist.
	KindNotFound
	// KindConflict is a request refused because of the state things are
	// in, such as removing the last admin.
	KindConflict
	// KindTooLarge is a request, or a thing asked for, past a size limit.
	KindTooLarge
)

type appError struct {
	kind    Kind
	message string
	err     error
}

func (e *appError) Error() string {
	if e.err != nil {
		return fmt.Sprintf("%s: %v", e.message, e.err)
	}
	return e.message
}

func (e *appError) Unwrap() error { return e.err }

// New creates an error carrying the given kind and a message safe to show
// to the person who made the request.
func New(kind Kind, message string) error {
	return &appError{kind: kind, message: message}
}

// Wrap attaches a kind and a public-facing message to an underlying error.
// The underlying error is preserved for logs but is never shown to the
// person who made the request.
func Wrap(kind Kind, message string, err error) error {
	return &appError{kind: kind, message: message, err: err}
}

// KindOf reports the kind of err, or KindInternal if err was not created by
// New or Wrap.
func KindOf(err error) Kind {
	var ae *appError
	if errors.As(err, &ae) {
		return ae.kind
	}
	return KindInternal
}

// PublicMessage returns the message safe to show to the person who made the
// request. For an error not created by New or Wrap, it returns a generic
// message rather than leaking internal detail.
func PublicMessage(err error) string {
	var ae *appError
	if errors.As(err, &ae) {
		return ae.message
	}
	return "Gitman could not complete this request"
}
