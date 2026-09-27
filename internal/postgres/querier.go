package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Querier is the subset of pgx shared by a pool and a transaction, so a
// function that reads or writes can run standalone or inside a caller's
// transaction without two versions of it.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Tx is a transaction handed to the function passed to DB.Tx.
type Tx = pgx.Tx

var (
	_ Querier = (*pgxpool.Pool)(nil)
	_ Querier = (pgx.Tx)(nil)
)
