package dialect

import (
	"context"
	"database/sql"
)

// Tx keeps transaction boundaries owned by database/sql while ensuring every
// statement in a shared repository is rendered for the selected backend.
type Tx struct {
	raw     *sql.Tx
	dialect Dialect
}

func WrapTx(tx *sql.Tx, selected Dialect) *Tx {
	return &Tx{raw: tx, dialect: selected}
}

func (tx *Tx) Raw() *sql.Tx {
	if tx == nil {
		return nil
	}
	return tx.raw
}

func (tx *Tx) IsMySQL() bool {
	return tx != nil && tx.dialect.IsMySQL()
}

func (tx *Tx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return tx.raw.ExecContext(ctx, tx.dialect.RewriteQuery(query), args...)
}

func (tx *Tx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return tx.raw.QueryContext(ctx, tx.dialect.RewriteQuery(query), args...)
}

func (tx *Tx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return tx.raw.QueryRowContext(ctx, tx.dialect.RewriteQuery(query), args...)
}

func (tx *Tx) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return tx.raw.PrepareContext(ctx, tx.dialect.RewriteQuery(query))
}

func (tx *Tx) Commit() error {
	return tx.raw.Commit()
}

func (tx *Tx) Rollback() error {
	return tx.raw.Rollback()
}
