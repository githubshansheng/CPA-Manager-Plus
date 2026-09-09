package databasemanagement

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"os"
	"syscall"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
)

const (
	// A derived-table rebuild is a replacement transaction: retrying it after
	// a connection failure is safe because the failed transaction is rolled
	// back and the next attempt deletes and rebuilds the complete table.
	derivedRebuildMaxAttempts = 3
	derivedRebuildRetryDelay  = 750 * time.Millisecond
)

func rebuildDerivedTableWithRetry(
	ctx context.Context,
	executor derivedPhaseExecutor,
	table string,
	watermark derivedRebuildWatermark,
) (int64, error) {
	var lastErr error
	for attempt := 1; attempt <= derivedRebuildMaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		rows, err := executor.RebuildTable(ctx, table, watermark)
		if err == nil {
			return rows, nil
		}
		lastErr = err
		if attempt == derivedRebuildMaxAttempts || !isRetryableDerivedConnectionError(err) {
			return 0, err
		}
		if err := waitDerivedRebuildRetry(ctx, attempt); err != nil {
			return 0, err
		}
	}
	return 0, lastErr
}

func waitDerivedRebuildRetry(ctx context.Context, attempt int) error {
	delay := derivedRebuildRetryDelay * time.Duration(1<<(attempt-1))
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// isRetryableDerivedConnectionError deliberately accepts only failures that
// indicate the connection, rather than the SQL statement, went away. Schema,
// permission, conversion, and syntax errors must never be hidden by a retry.
func isRetryableDerivedConnectionError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, mysqldriver.ErrInvalidConn) || errors.Is(err, driver.ErrBadConn) ||
		errors.Is(err, sql.ErrConnDone) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) {
		return networkErr.Timeout() || networkErr.Temporary()
	}
	var syscallErr *os.SyscallError
	if errors.As(err, &syscallErr) {
		return errors.Is(syscallErr, syscall.ECONNRESET) ||
			errors.Is(syscallErr, syscall.ECONNABORTED) ||
			errors.Is(syscallErr, syscall.ENETUNREACH) ||
			errors.Is(syscallErr, syscall.EHOSTUNREACH) ||
			errors.Is(syscallErr, syscall.ETIMEDOUT) ||
			errors.Is(syscallErr, syscall.EPIPE)
	}
	var mysqlErr *mysqldriver.MySQLError
	if errors.As(err, &mysqlErr) {
		switch mysqlErr.Number {
		case 1040, // ER_CON_COUNT_ERROR
			1053, // ER_SERVER_SHUTDOWN
			2002, // CR_CONNECTION_ERROR
			2003, // CR_CONN_HOST_ERROR
			2006, // CR_SERVER_GONE_ERROR
			2013: // CR_SERVER_LOST
			return true
		}
	}
	return false
}
