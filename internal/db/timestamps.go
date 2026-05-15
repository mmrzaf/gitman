package db

import (
	"database/sql"
	"time"
)

func unixToTime(sec int64) time.Time {
	return time.Unix(sec, 0)
}

func nullUnixToTime(sec sql.NullInt64) *time.Time {
	if !sec.Valid {
		return nil
	}
	t := time.Unix(sec.Int64, 0)
	return &t
}
