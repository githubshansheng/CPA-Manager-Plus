package databasemigration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

func HashIdempotencyRequest(request any) (string, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("encode idempotent request: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func (r *SQLRepository) OperationIdempotency(ctx context.Context, key string) (OperationIdempotencyRecord, error) {
	record, err := scanOperationIdempotency(r.db.QueryRowContext(ctx, `SELECT idempotency_key,
		operation_name, request_hash, result_json, result_generation, created_at_ms, updated_at_ms
		FROM database_operation_idempotency WHERE idempotency_key = ?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return OperationIdempotencyRecord{}, ErrNotFound
	}
	return record, err
}

// StoreOperationIdempotency persists the completed response of a management
// operation. Reusing the key with the same operation and request hash returns
// the first durable result; reusing it for different input is a conflict. This
// lets HTTP handlers safely replay state-changing results after a restart.
func (r *SQLRepository) StoreOperationIdempotency(ctx context.Context, record OperationIdempotencyRecord) (OperationIdempotencyRecord, bool, error) {
	if record.Key == "" || record.Operation == "" || record.RequestHash == "" ||
		len(record.Result) == 0 || !json.Valid(record.Result) {
		return OperationIdempotencyRecord{}, false, errors.New("idempotency record requires key, operation, request hash, and valid JSON result")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return OperationIdempotencyRecord{}, false, err
	}
	defer tx.Rollback()
	existing, err := scanOperationIdempotency(tx.QueryRowContext(ctx, `SELECT idempotency_key,
		operation_name, request_hash, result_json, result_generation, created_at_ms, updated_at_ms
		FROM database_operation_idempotency WHERE idempotency_key = ?`, record.Key))
	if err == nil {
		if existing.Operation != record.Operation || existing.RequestHash != record.RequestHash {
			return OperationIdempotencyRecord{}, false, ErrIdempotencyConflict
		}
		return existing, false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return OperationIdempotencyRecord{}, false, err
	}
	nowMS := r.now().UnixMilli()
	record.CreatedAtMS = nowMS
	record.UpdatedAtMS = nowMS
	_, err = tx.ExecContext(ctx, `INSERT INTO database_operation_idempotency
		(idempotency_key, operation_name, request_hash, result_json, result_generation,
		created_at_ms, updated_at_ms) VALUES (?, ?, ?, ?, ?, ?, ?)`, record.Key,
		record.Operation, record.RequestHash, string(record.Result), record.Generation,
		record.CreatedAtMS, record.UpdatedAtMS)
	if err != nil {
		// A concurrent request may have won the unique-key race. Re-read after
		// releasing this transaction and apply the same strict comparison.
		_ = tx.Rollback()
		existing, readErr := r.OperationIdempotency(ctx, record.Key)
		if readErr != nil {
			return OperationIdempotencyRecord{}, false, err
		}
		if existing.Operation != record.Operation || existing.RequestHash != record.RequestHash {
			return OperationIdempotencyRecord{}, false, ErrIdempotencyConflict
		}
		return existing, false, nil
	}
	if err := tx.Commit(); err != nil {
		return OperationIdempotencyRecord{}, false, err
	}
	return record, true, nil
}

func scanOperationIdempotency(row interface{ Scan(...any) error }) (OperationIdempotencyRecord, error) {
	var record OperationIdempotencyRecord
	var result string
	err := row.Scan(&record.Key, &record.Operation, &record.RequestHash, &result,
		&record.Generation, &record.CreatedAtMS, &record.UpdatedAtMS)
	if err != nil {
		return OperationIdempotencyRecord{}, err
	}
	record.Result = json.RawMessage(result)
	if !json.Valid(record.Result) {
		return OperationIdempotencyRecord{}, errors.New("stored idempotency result is invalid JSON")
	}
	return record, nil
}
