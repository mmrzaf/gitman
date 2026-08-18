package db

import (
	"database/sql"
	"errors"
)

// ErrNotFound is returned by single-object lookups when no matching row exists.
// Callers should use errors.Is(err, db.ErrNotFound).
var (
	ErrNotFound      = errors.New("not found")
	ErrAlreadyExists = errors.New("already exists")
	ErrActiveCIRuns  = errors.New("active CI runs")
)

func normalizeNotFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func requireAffectedRow(res sql.Result, noRowsErr error) error {
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return noRowsErr
	}
	return nil
}
