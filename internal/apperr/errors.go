package apperr

import (
	"errors"
	"fmt"
)

type Kind uint8

const (
	KindInternal Kind = iota
	KindInvalid
	KindUnauthenticated
	KindForbidden
	KindNotFound
	KindConflict
	KindTooLarge
	KindUnsupported
	KindMethodNotAllowed
	KindUnavailable
)

type Error struct {
	Kind    Kind
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" && e.Err != nil {
		return e.Message + ": " + e.Err.Error()
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return "application error"
}

func (e *Error) Unwrap() error { return e.Err }

func New(kind Kind, message string) error {
	return &Error{Kind: kind, Message: message}
}

func Wrap(kind Kind, message string, err error) error {
	// A public application failure must never disappear merely because its
	// optional underlying cause is nil. Callers use New when there is no cause,
	// but keeping Wrap non-nil makes accidental nil causes fail safe.
	return &Error{Kind: kind, Message: message, Err: err}
}

func KindOf(err error) Kind {
	var appErr *Error
	if errors.As(err, &appErr) {
		return appErr.Kind
	}
	return KindInternal
}

func PublicMessage(err error) string {
	var appErr *Error
	if errors.As(err, &appErr) && appErr.Message != "" {
		return appErr.Message
	}
	return "Gitman could not complete this request"
}

func Is(err error, kind Kind) bool { return KindOf(err) == kind }

func (k Kind) String() string {
	switch k {
	case KindInvalid:
		return "invalid"
	case KindUnauthenticated:
		return "unauthenticated"
	case KindForbidden:
		return "forbidden"
	case KindNotFound:
		return "not_found"
	case KindConflict:
		return "conflict"
	case KindTooLarge:
		return "too_large"
	case KindUnsupported:
		return "unsupported"
	case KindMethodNotAllowed:
		return "method_not_allowed"
	case KindUnavailable:
		return "unavailable"
	case KindInternal:
		return "internal"
	default:
		return fmt.Sprintf("kind_%d", k)
	}
}
