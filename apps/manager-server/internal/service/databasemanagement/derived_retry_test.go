package databasemanagement

import (
	"context"
	"errors"
	"testing"

	mysqldriver "github.com/go-sql-driver/mysql"
)

type retryTestDerivedExecutor struct {
	attempts int
	failures []error
}

func (e *retryTestDerivedExecutor) Tables() []string { return []string{"derived"} }

func (e *retryTestDerivedExecutor) CaptureWatermark(context.Context) (derivedRebuildWatermark, error) {
	return derivedRebuildWatermark{UsageEventID: 1, PricingRevision: "test"}, nil
}

func (e *retryTestDerivedExecutor) RebuildTable(context.Context, string, derivedRebuildWatermark) (int64, error) {
	e.attempts++
	if len(e.failures) > 0 {
		err := e.failures[0]
		e.failures = e.failures[1:]
		return 0, err
	}
	return 7, nil
}

func TestRebuildDerivedTableRetriesConnectionLoss(t *testing.T) {
	executor := &retryTestDerivedExecutor{failures: []error{mysqldriver.ErrInvalidConn}}
	rows, err := rebuildDerivedTableWithRetry(
		context.Background(), executor, "derived", derivedRebuildWatermark{UsageEventID: 1, PricingRevision: "test"},
	)
	if err != nil || rows != 7 {
		t.Fatalf("retry result rows=%d err=%v", rows, err)
	}
	if executor.attempts != 2 {
		t.Fatalf("rebuild attempts=%d, want 2", executor.attempts)
	}
}

func TestRebuildDerivedTableDoesNotRetryStatementErrors(t *testing.T) {
	statementErr := errors.New("unknown column in expression")
	executor := &retryTestDerivedExecutor{failures: []error{statementErr}}
	_, err := rebuildDerivedTableWithRetry(
		context.Background(), executor, "derived", derivedRebuildWatermark{UsageEventID: 1, PricingRevision: "test"},
	)
	if !errors.Is(err, statementErr) {
		t.Fatalf("statement error=%v, want %v", err, statementErr)
	}
	if executor.attempts != 1 {
		t.Fatalf("statement error was retried %d times", executor.attempts)
	}
}

func TestRetryableDerivedConnectionErrorClassification(t *testing.T) {
	if !isRetryableDerivedConnectionError(mysqldriver.ErrInvalidConn) {
		t.Fatal("mysql invalid connection was not classified as retryable")
	}
	if isRetryableDerivedConnectionError(&mysqldriver.MySQLError{Number: 1054, Message: "unknown column"}) {
		t.Fatal("schema error was classified as retryable")
	}
}
