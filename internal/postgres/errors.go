package postgres

import (
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrNotFound is returned when a lookup by ID or unique key matches no row.
var ErrNotFound = errors.New("not found")

// ErrAlreadyExists is returned when an insert would violate a uniqueness
// constraint the caller is expected to handle, such as a repository name
// or a token hash that is already taken.
var ErrAlreadyExists = errors.New("already exists")

// IsUniqueViolation reports whether err is a PostgreSQL unique-constraint
// violation, regardless of which constraint it was.
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// NormalizeNotFound turns pgx.ErrNoRows into ErrNotFound, so callers
// outside this package never depend on a driver-specific error value.
// Every package that queries the database runs its lookup errors through
// this function, so "not found" means exactly one thing everywhere.
func NormalizeNotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// NormalizeWrite maps a unique-constraint violation to ErrAlreadyExists and
// leaves every other error untouched.
func NormalizeWrite(err error) error {
	if IsUniqueViolation(err) {
		return ErrAlreadyExists
	}
	return err
}
