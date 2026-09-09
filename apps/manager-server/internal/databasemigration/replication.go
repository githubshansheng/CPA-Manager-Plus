package databasemigration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type MutationApplier func(ctx context.Context, tx *sql.Tx, mutation Mutation) error

type InboxApplyResult struct {
	Applied   bool
	Watermark int64
}

func ValidateMutationGroup(group MutationGroup) error {
	if group.TransactionID == "" || !validBackend(group.Source) || !validBackend(group.Target) ||
		group.Source == group.Target || group.SourceEpoch <= 0 || len(group.Mutations) == 0 {
		return fmt.Errorf("%w: transaction, distinct backends, positive epoch, and mutations are required", ErrInvalidMutationGroup)
	}
	seen := make(map[string]struct{}, len(group.Mutations))
	for index, mutation := range group.Mutations {
		if mutation.ID == "" || mutation.Table == "" || len(mutation.PrimaryKey) == 0 || mutation.SchemaVersion <= 0 {
			return fmt.Errorf("%w: mutation %d is incomplete", ErrInvalidMutationGroup, index)
		}
		if !json.Valid(mutation.PrimaryKey) {
			return fmt.Errorf("%w: mutation %d primary key is not valid JSON", ErrInvalidMutationGroup, index)
		}
		if mutation.Sequence != index {
			return fmt.Errorf("%w: mutation sequence %d, want %d", ErrInvalidMutationGroup, mutation.Sequence, index)
		}
		if mutation.TransactionID != "" && mutation.TransactionID != group.TransactionID ||
			mutation.Source != "" && mutation.Source != group.Source ||
			mutation.Target != "" && mutation.Target != group.Target ||
			mutation.SourceEpoch != 0 && mutation.SourceEpoch != group.SourceEpoch {
			return fmt.Errorf("%w: mutation %d does not belong to its transaction group", ErrInvalidMutationGroup, index)
		}
		switch mutation.Operation {
		case OperationInsert, OperationUpdate:
			if len(mutation.Payload) == 0 || !json.Valid(mutation.Payload) {
				return fmt.Errorf("%w: %s mutation %d has no full-field payload", ErrInvalidMutationGroup, mutation.Operation, index)
			}
		case OperationDelete:
		default:
			return fmt.Errorf("%w: mutation %d has invalid operation %q", ErrInvalidMutationGroup, index, mutation.Operation)
		}
		if _, exists := seen[mutation.ID]; exists {
			return fmt.Errorf("%w: duplicate mutation id %q", ErrInvalidMutationGroup, mutation.ID)
		}
		seen[mutation.ID] = struct{}{}
	}
	return nil
}

func normalizeGroup(group MutationGroup, nowMS int64) MutationGroup {
	result := group
	result.Mutations = append([]Mutation(nil), group.Mutations...)
	for index := range result.Mutations {
		mutation := &result.Mutations[index]
		mutation.TransactionID = group.TransactionID
		mutation.Source = group.Source
		mutation.Target = group.Target
		mutation.SourceEpoch = group.SourceEpoch
		if mutation.CreatedAtMS == 0 {
			mutation.CreatedAtMS = nowMS
		}
	}
	return result
}

// AppendOutbox appends the reliable transaction group using the caller's
// authoritative write transaction. Returning an error is intended to abort
// that same transaction, so an authoritative mutation cannot commit without
// its Outbox record.
func (r *SQLRepository) AppendOutbox(ctx context.Context, tx *sql.Tx, group MutationGroup) error {
	if tx == nil {
		return errors.New("outbox append requires the authoritative sql transaction")
	}
	group = normalizeGroup(group, r.now().UnixMilli())
	if err := ValidateMutationGroup(group); err != nil {
		return err
	}
	routing, err := scanRouting(tx.QueryRowContext(ctx, `SELECT generation, write_primary,
		business_read, system_read, epoch, updated_at_ms FROM database_routing_state WHERE id = 1`))
	if err != nil {
		return err
	}
	if routing.WritePrimary != group.Source || routing.Epoch != group.SourceEpoch {
		return &ConflictError{Kind: ErrEpochFenced, Expected: group.SourceEpoch, Actual: routing.Epoch}
	}
	existing := 0
	for _, mutation := range group.Mutations {
		digest := MutationDigest(mutation)
		var stored string
		err := tx.QueryRowContext(ctx, `SELECT mutation_digest FROM database_outbox WHERE mutation_id = ?`, mutation.ID).Scan(&stored)
		if err == nil {
			if stored != digest {
				return fmt.Errorf("%w: mutation id %q has different contents", ErrInvalidMutationGroup, mutation.ID)
			}
			existing++
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	if existing != 0 {
		if existing == len(group.Mutations) {
			return nil
		}
		return ErrPartialDuplicate
	}
	for _, mutation := range group.Mutations {
		payloadBytes := len(mutation.PrimaryKey) + len(mutation.Payload)
		if _, err := tx.ExecContext(ctx, `INSERT INTO database_outbox (mutation_id,
			mutation_digest, transaction_id, sequence_no, source_backend, target_backend,
			source_epoch, table_name, mutation_operation, primary_key_json, payload_json,
			schema_version, row_version, payload_bytes, created_at_ms, applied_at_ms)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`, mutation.ID,
			MutationDigest(mutation), group.TransactionID, mutation.Sequence, group.Source,
			group.Target, group.SourceEpoch, mutation.Table, mutation.Operation,
			string(mutation.PrimaryKey), nullableString(mutation.Payload), mutation.SchemaVersion,
			mutation.RowVersion, payloadBytes, mutation.CreatedAtMS); err != nil {
			return fmt.Errorf("append outbox mutation %q: %w", mutation.ID, err)
		}
	}
	return nil
}

func (r *SQLRepository) PendingOutbox(ctx context.Context, maxGroups int) ([]MutationGroup, error) {
	if maxGroups <= 0 {
		maxGroups = 1
	}
	rows, err := r.db.QueryContext(ctx, `SELECT transaction_id, MIN(outbox_id) AS first_id
		FROM database_outbox WHERE applied_at_ms = 0 GROUP BY transaction_id
		ORDER BY first_id LIMIT ?`, maxGroups)
	if err != nil {
		return nil, err
	}
	var transactionIDs []string
	for rows.Next() {
		var id string
		var ignored int64
		if err := rows.Scan(&id, &ignored); err != nil {
			rows.Close()
			return nil, err
		}
		transactionIDs = append(transactionIDs, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	groups := make([]MutationGroup, 0, len(transactionIDs))
	for _, transactionID := range transactionIDs {
		group, err := r.readOutboxGroup(ctx, transactionID)
		if err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}
	return groups, nil
}

func (r *SQLRepository) readOutboxGroup(ctx context.Context, transactionID string) (MutationGroup, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT outbox_id, mutation_id, mutation_digest, transaction_id,
		sequence_no, source_backend, target_backend, source_epoch, table_name,
		mutation_operation, primary_key_json, payload_json, schema_version, row_version,
		created_at_ms FROM database_outbox WHERE transaction_id = ? AND applied_at_ms = 0
		ORDER BY sequence_no`, transactionID)
	if err != nil {
		return MutationGroup{}, err
	}
	defer rows.Close()
	var group MutationGroup
	for rows.Next() {
		var mutation Mutation
		var storedDigest string
		var key string
		var payload sql.NullString
		if err := rows.Scan(&mutation.OutboxID, &mutation.ID, &storedDigest, &mutation.TransactionID,
			&mutation.Sequence, &mutation.Source, &mutation.Target, &mutation.SourceEpoch,
			&mutation.Table, &mutation.Operation, &key, &payload, &mutation.SchemaVersion,
			&mutation.RowVersion, &mutation.CreatedAtMS); err != nil {
			return MutationGroup{}, err
		}
		mutation.PrimaryKey = []byte(key)
		if payload.Valid {
			mutation.Payload = []byte(payload.String)
		}
		if actual := MutationDigest(mutation); storedDigest != actual {
			return MutationGroup{}, fmt.Errorf("stored outbox mutation %q digest mismatch: got %s, want %s",
				mutation.ID, storedDigest, actual)
		}
		if len(group.Mutations) == 0 {
			group = MutationGroup{TransactionID: mutation.TransactionID, Source: mutation.Source,
				Target: mutation.Target, SourceEpoch: mutation.SourceEpoch}
		}
		group.Watermark = max(group.Watermark, mutation.OutboxID)
		group.Mutations = append(group.Mutations, mutation)
	}
	if err := rows.Err(); err != nil {
		return MutationGroup{}, err
	}
	if err := ValidateMutationGroup(group); err != nil {
		return MutationGroup{}, fmt.Errorf("stored outbox group is invalid: %w", err)
	}
	return group, nil
}

func (r *SQLRepository) MarkOutboxApplied(ctx context.Context, group MutationGroup, appliedAt time.Time) error {
	if err := ValidateMutationGroup(group); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, mutation := range group.Mutations {
		result, err := tx.ExecContext(ctx, `UPDATE database_outbox SET applied_at_ms = ?
			WHERE mutation_id = ? AND applied_at_ms = 0`, appliedAt.UnixMilli(), mutation.ID)
		if err != nil {
			return err
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			var stored int64
			if err := tx.QueryRowContext(ctx, `SELECT applied_at_ms FROM database_outbox WHERE mutation_id = ?`, mutation.ID).Scan(&stored); err != nil {
				return err
			}
			if stored == 0 {
				return fmt.Errorf("outbox mutation %q was not acknowledged", mutation.ID)
			}
		}
	}
	return tx.Commit()
}

func (r *SQLRepository) OutboxBacklog(ctx context.Context) (rowsCount, bytesCount, oldestAtMS, sourceWatermark int64, err error) {
	err = r.db.QueryRowContext(ctx, `SELECT
		COUNT(CASE WHEN applied_at_ms = 0 THEN 1 END),
		COALESCE(SUM(CASE WHEN applied_at_ms = 0 THEN payload_bytes ELSE 0 END), 0),
		COALESCE(MIN(CASE WHEN applied_at_ms = 0 THEN created_at_ms END), 0),
		COALESCE(MAX(outbox_id), 0) FROM database_outbox`).Scan(
		&rowsCount, &bytesCount, &oldestAtMS, &sourceWatermark)
	return
}

func (r *SQLRepository) InboxWatermark(ctx context.Context, source Backend) (int64, error) {
	if r == nil || r.db == nil || !validBackend(source) {
		return 0, errors.New("inbox watermark requires a repository and valid source backend")
	}
	var watermark int64
	err := r.db.QueryRowContext(ctx, `SELECT watermark FROM database_inbox_sources
		WHERE source_backend = ?`, source).Scan(&watermark)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return watermark, err
}

func (r *SQLRepository) ApplyInboxGroup(ctx context.Context, group MutationGroup, applier MutationApplier) (InboxApplyResult, error) {
	group = normalizeGroup(group, r.now().UnixMilli())
	if err := ValidateMutationGroup(group); err != nil {
		return InboxApplyResult{}, err
	}
	if applier == nil {
		return InboxApplyResult{}, errors.New("inbox apply requires a mutation applier")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return InboxApplyResult{}, err
	}
	defer tx.Rollback()
	result, err := r.ApplyInboxGroupTx(ctx, tx, group, applier)
	if err != nil {
		return InboxApplyResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return InboxApplyResult{}, err
	}
	return result, nil
}

// ApplyInboxGroupTx applies an idempotent transaction group through a caller-
// owned transaction. It is used by SQLite reverse replication so journal
// triggers can be suppressed and restored in the same atomic transaction as
// the Inbox, row-version tombstones and authoritative rows. The caller alone
// is responsible for commit or rollback.
func (r *SQLRepository) ApplyInboxGroupTx(
	ctx context.Context,
	tx *sql.Tx,
	group MutationGroup,
	applier MutationApplier,
) (InboxApplyResult, error) {
	if tx == nil {
		return InboxApplyResult{}, errors.New("inbox apply requires a caller transaction")
	}
	group = normalizeGroup(group, r.now().UnixMilli())
	if err := ValidateMutationGroup(group); err != nil {
		return InboxApplyResult{}, err
	}
	if applier == nil {
		return InboxApplyResult{}, errors.New("inbox apply requires a mutation applier")
	}
	var acceptedEpoch, watermark int64
	err := tx.QueryRowContext(ctx, `SELECT accepted_epoch, watermark FROM database_inbox_sources
		WHERE source_backend = ?`, group.Source).Scan(&acceptedEpoch, &watermark)
	if errors.Is(err, sql.ErrNoRows) {
		acceptedEpoch = group.SourceEpoch
		if _, err := tx.ExecContext(ctx, `INSERT INTO database_inbox_sources
			(source_backend, accepted_epoch, watermark, updated_at_ms) VALUES (?, ?, 0, ?)`,
			group.Source, acceptedEpoch, r.now().UnixMilli()); err != nil {
			return InboxApplyResult{}, err
		}
	} else if err != nil {
		return InboxApplyResult{}, err
	}
	existing := 0
	for _, mutation := range group.Mutations {
		var digest string
		err := tx.QueryRowContext(ctx, `SELECT mutation_digest FROM database_inbox WHERE mutation_id = ?`, mutation.ID).Scan(&digest)
		if err == nil {
			if digest != MutationDigest(mutation) {
				return InboxApplyResult{}, fmt.Errorf("%w: inbox mutation %q has different contents", ErrInvalidMutationGroup, mutation.ID)
			}
			existing++
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return InboxApplyResult{}, err
		}
	}
	if existing != 0 && existing != len(group.Mutations) {
		return InboxApplyResult{}, ErrPartialDuplicate
	}
	if existing == len(group.Mutations) {
		return InboxApplyResult{Applied: false, Watermark: watermark}, nil
	}
	if group.SourceEpoch < acceptedEpoch {
		return InboxApplyResult{}, &ConflictError{Kind: ErrEpochFenced, Expected: group.SourceEpoch, Actual: acceptedEpoch}
	}
	if group.SourceEpoch > acceptedEpoch {
		acceptedEpoch = group.SourceEpoch
	}
	for _, mutation := range group.Mutations {
		accepted, err := r.AcceptRowVersion(ctx, tx, RowVersionRecord{Table: mutation.Table,
			PrimaryKey: mutation.PrimaryKey, SourceEpoch: mutation.SourceEpoch, RowVersion: mutation.RowVersion,
			MutationID: mutation.ID, MutationHash: MutationDigest(mutation)})
		if err != nil {
			return InboxApplyResult{}, err
		}
		if accepted {
			if err := applier(ctx, tx, mutation); err != nil {
				return InboxApplyResult{}, err
			}
		}
	}
	nowMS := r.now().UnixMilli()
	for _, mutation := range group.Mutations {
		if _, err := tx.ExecContext(ctx, `INSERT INTO database_inbox (mutation_id,
			mutation_digest, transaction_id, sequence_no, source_backend, source_epoch,
			outbox_id, applied_at_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, mutation.ID,
			MutationDigest(mutation), group.TransactionID, mutation.Sequence, group.Source,
			group.SourceEpoch, mutation.OutboxID, nowMS); err != nil {
			return InboxApplyResult{}, err
		}
	}
	watermark = max(watermark, group.Watermark)
	if _, err := tx.ExecContext(ctx, `UPDATE database_inbox_sources SET accepted_epoch = ?,
		watermark = ?, updated_at_ms = ? WHERE source_backend = ?`, acceptedEpoch, watermark,
		nowMS, group.Source); err != nil {
		return InboxApplyResult{}, err
	}
	return InboxApplyResult{Applied: true, Watermark: watermark}, nil
}

func MutationDigest(mutation Mutation) string {
	primaryKey, _ := canonicalJSON(mutation.PrimaryKey)
	payload, _ := canonicalJSON(mutation.Payload)
	hash := sha256.New()
	fields := [][]byte{
		[]byte(mutation.ID), []byte(mutation.TransactionID), []byte(fmt.Sprint(mutation.Sequence)),
		[]byte(mutation.Source), []byte(mutation.Target), []byte(fmt.Sprint(mutation.SourceEpoch)),
		[]byte(mutation.Table), []byte(mutation.Operation), primaryKey, payload,
		[]byte(fmt.Sprint(mutation.SchemaVersion)), []byte(fmt.Sprint(mutation.RowVersion)),
	}
	var length [8]byte
	for _, field := range fields {
		binary.BigEndian.PutUint64(length[:], uint64(len(field)))
		hash.Write(length[:])
		hash.Write(field)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// AcceptRowVersion compares and durably records a row's fencing tuple inside
// the caller's target transaction. Real-time Inbox appliers and historical
// copy targets share this helper, so a historical batch cannot overwrite a
// newer row. Deletes must also record a version, preserving a tombstone.
func (r *SQLRepository) AcceptRowVersion(ctx context.Context, tx *sql.Tx, candidate RowVersionRecord) (bool, error) {
	return r.acceptRowVersion(ctx, tx, candidate, false)
}

// AcceptHistoricalRowVersion applies the normal epoch/version fence while
// allowing one narrow recovery case: a history snapshot may replace an older
// history snapshot bound to the same epoch and synthetic version. This is
// needed when an earlier history batch committed at the target but its source
// checkpoint did not advance, or when a pre-Outbox startup migration changed
// a row before the task resumed. Real-time mutations are never eligible for
// this rebind, and a higher real-time version still wins.
func (r *SQLRepository) AcceptHistoricalRowVersion(ctx context.Context, tx *sql.Tx, candidate RowVersionRecord) (bool, error) {
	return r.acceptRowVersion(ctx, tx, candidate, true)
}

func (r *SQLRepository) acceptRowVersion(
	ctx context.Context,
	tx *sql.Tx,
	candidate RowVersionRecord,
	allowHistoryRebind bool,
) (bool, error) {
	if tx == nil || candidate.Table == "" || len(candidate.PrimaryKey) == 0 ||
		!json.Valid(candidate.PrimaryKey) || candidate.SourceEpoch <= 0 || candidate.RowVersion < 0 ||
		candidate.MutationID == "" || candidate.MutationHash == "" {
		return false, errors.New("row version requires a transaction, table, JSON primary key, epoch, version, mutation id, and digest")
	}
	canonicalKey, err := canonicalJSON(candidate.PrimaryKey)
	if err != nil {
		return false, err
	}
	keyDigest := sha256.Sum256(canonicalKey)
	keyHash := hex.EncodeToString(keyDigest[:])
	var stored RowVersionRecord
	var storedKey string
	err = tx.QueryRowContext(ctx, `SELECT table_name, primary_key_json, source_epoch,
		row_version, mutation_id, mutation_digest, updated_at_ms FROM database_row_versions
		WHERE table_name = ? AND primary_key_hash = ?`, candidate.Table, keyHash).Scan(
		&stored.Table, &storedKey, &stored.SourceEpoch, &stored.RowVersion, &stored.MutationID,
		&stored.MutationHash, &stored.UpdatedAtMS)
	if errors.Is(err, sql.ErrNoRows) {
		candidate.UpdatedAtMS = r.now().UnixMilli()
		_, err = tx.ExecContext(ctx, `INSERT INTO database_row_versions (table_name,
			primary_key_hash, primary_key_json, source_epoch, row_version, mutation_id,
			mutation_digest, updated_at_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, candidate.Table,
			keyHash, string(canonicalKey), candidate.SourceEpoch, candidate.RowVersion,
			candidate.MutationID, candidate.MutationHash, candidate.UpdatedAtMS)
		return err == nil, err
	}
	if err != nil {
		return false, err
	}
	if storedKey != string(canonicalKey) {
		return false, errors.New("primary key hash collision")
	}
	if stored.SourceEpoch > candidate.SourceEpoch ||
		stored.SourceEpoch == candidate.SourceEpoch && stored.RowVersion > candidate.RowVersion {
		return false, nil
	}
	if stored.SourceEpoch == candidate.SourceEpoch && stored.RowVersion == candidate.RowVersion {
		if stored.MutationHash != candidate.MutationHash {
			if !allowHistoryRebind || !isHistoricalMutationID(stored.MutationID) ||
				!isHistoricalMutationID(candidate.MutationID) {
				return false, fmt.Errorf("%w: table %q key %s epoch %d version %d", ErrRowVersionConflict,
					candidate.Table, canonicalKey, candidate.SourceEpoch, candidate.RowVersion)
			}
		} else {
			return false, nil
		}
	}
	candidate.UpdatedAtMS = r.now().UnixMilli()
	_, err = tx.ExecContext(ctx, `UPDATE database_row_versions SET primary_key_json = ?,
		source_epoch = ?, row_version = ?, mutation_id = ?, mutation_digest = ?, updated_at_ms = ?
		WHERE table_name = ? AND primary_key_hash = ?`, string(canonicalKey), candidate.SourceEpoch,
		candidate.RowVersion, candidate.MutationID, candidate.MutationHash, candidate.UpdatedAtMS,
		candidate.Table, keyHash)
	return err == nil, err
}

func isHistoricalMutationID(value string) bool {
	digest, ok := strings.CutPrefix(value, "history-")
	if !ok || len(digest) != 32 && len(digest) != 64 {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil
}

func (r *SQLRepository) RowVersion(ctx context.Context, table string, primaryKey json.RawMessage) (RowVersionRecord, error) {
	canonicalKey, err := canonicalJSON(primaryKey)
	if err != nil {
		return RowVersionRecord{}, err
	}
	digest := sha256.Sum256(canonicalKey)
	var record RowVersionRecord
	var key string
	err = r.db.QueryRowContext(ctx, `SELECT table_name, primary_key_json, source_epoch,
		row_version, mutation_id, mutation_digest, updated_at_ms FROM database_row_versions
		WHERE table_name = ? AND primary_key_hash = ?`, table, hex.EncodeToString(digest[:])).Scan(
		&record.Table, &key, &record.SourceEpoch, &record.RowVersion, &record.MutationID,
		&record.MutationHash, &record.UpdatedAtMS)
	if errors.Is(err, sql.ErrNoRows) {
		return RowVersionRecord{}, ErrNotFound
	}
	if err != nil {
		return RowVersionRecord{}, err
	}
	record.PrimaryKey = json.RawMessage(key)
	return record, nil
}

func canonicalJSON(value json.RawMessage) ([]byte, error) {
	if len(value) == 0 {
		return nil, nil
	}
	var decoded any
	if err := json.Unmarshal(value, &decoded); err != nil {
		return nil, err
	}
	return json.Marshal(decoded)
}

func (r *SQLRepository) UpdateReplication(ctx context.Context, update ReplicationUpdate) (ReplicationState, error) {
	if update.Direction == "" || !validBackend(update.Source) || !validBackend(update.Target) || update.Source == update.Target {
		return ReplicationState{}, errors.New("replication update has invalid direction or backends")
	}
	nowMS := r.now().UnixMilli()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return ReplicationState{}, err
	}
	defer tx.Rollback()
	current, found, err := readReplicationTx(ctx, tx, update.Direction)
	if err != nil {
		return ReplicationState{}, err
	}
	state := ReplicationState{Direction: update.Direction, Source: update.Source, Target: update.Target,
		Epoch: update.Epoch, SourceWatermark: update.SourceWatermark, TargetWatermark: update.TargetWatermark,
		BacklogRows: update.BacklogRows, BacklogBytes: update.BacklogBytes,
		OldestBacklogAtMS: update.OldestBacklogAtMS, ThroughputRowsPerSec: update.ThroughputRowsPerSec,
		Retries: update.Retries, HeartbeatAtMS: nowMS, LastError: update.LastError}
	if found {
		if update.Epoch < current.Epoch {
			return ReplicationState{}, &ConflictError{Kind: ErrEpochFenced, Expected: update.Epoch, Actual: current.Epoch}
		}
		if update.Epoch == current.Epoch && (update.SourceWatermark < current.SourceWatermark ||
			update.TargetWatermark < current.TargetWatermark) {
			return ReplicationState{}, errors.New("replication watermarks cannot regress within an epoch")
		}
		state.LastProgressAtMS = current.LastProgressAtMS
		state.LastSuccessAtMS = current.LastSuccessAtMS
	} else {
		state.LastProgressAtMS = nowMS
	}
	if update.MadeProgress || state.BacklogRows == 0 {
		state.LastProgressAtMS = nowMS
	}
	if update.Succeeded {
		state.LastSuccessAtMS = nowMS
	}
	if found {
		_, err = tx.ExecContext(ctx, `UPDATE database_replication_state SET source_backend = ?,
			target_backend = ?, epoch = ?, source_watermark = ?, target_watermark = ?,
			backlog_rows = ?, backlog_bytes = ?, oldest_backlog_at_ms = ?, throughput_rows_per_sec = ?,
			retries = ?, heartbeat_at_ms = ?, last_progress_at_ms = ?, last_success_at_ms = ?, last_error = ?
			WHERE direction = ?`, state.Source, state.Target, state.Epoch, state.SourceWatermark,
			state.TargetWatermark, state.BacklogRows, state.BacklogBytes, state.OldestBacklogAtMS,
			state.ThroughputRowsPerSec, state.Retries, state.HeartbeatAtMS, state.LastProgressAtMS,
			state.LastSuccessAtMS, state.LastError, state.Direction)
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO database_replication_state (direction,
			source_backend, target_backend, epoch, source_watermark, target_watermark, backlog_rows,
			backlog_bytes, oldest_backlog_at_ms, throughput_rows_per_sec, retries, heartbeat_at_ms,
			last_progress_at_ms, last_success_at_ms, last_error) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			state.Direction, state.Source, state.Target, state.Epoch, state.SourceWatermark,
			state.TargetWatermark, state.BacklogRows, state.BacklogBytes, state.OldestBacklogAtMS,
			state.ThroughputRowsPerSec, state.Retries, state.HeartbeatAtMS, state.LastProgressAtMS,
			state.LastSuccessAtMS, state.LastError)
	}
	if err != nil {
		return ReplicationState{}, err
	}
	if err := tx.Commit(); err != nil {
		return ReplicationState{}, err
	}
	return ReplicationHealth(state, r.now()), nil
}

func (r *SQLRepository) Replication(ctx context.Context, direction string) (ReplicationState, error) {
	state, err := scanReplication(r.db.QueryRowContext(ctx, replicationSelect+` WHERE direction = ?`, direction))
	if errors.Is(err, sql.ErrNoRows) {
		return ReplicationState{}, ErrNotFound
	}
	if err != nil {
		return ReplicationState{}, err
	}
	return ReplicationHealth(state, r.now()), nil
}

const replicationSelect = `SELECT direction, source_backend, target_backend, epoch,
	source_watermark, target_watermark, backlog_rows, backlog_bytes, oldest_backlog_at_ms,
	throughput_rows_per_sec, retries, heartbeat_at_ms, last_progress_at_ms, last_success_at_ms,
	last_error FROM database_replication_state`

func readReplicationTx(ctx context.Context, tx *sql.Tx, direction string) (ReplicationState, bool, error) {
	state, err := scanReplication(tx.QueryRowContext(ctx, replicationSelect+` WHERE direction = ?`, direction))
	if errors.Is(err, sql.ErrNoRows) {
		return ReplicationState{}, false, nil
	}
	return state, err == nil, err
}

func scanReplication(row interface{ Scan(...any) error }) (ReplicationState, error) {
	var state ReplicationState
	err := row.Scan(&state.Direction, &state.Source, &state.Target, &state.Epoch,
		&state.SourceWatermark, &state.TargetWatermark, &state.BacklogRows, &state.BacklogBytes,
		&state.OldestBacklogAtMS, &state.ThroughputRowsPerSec, &state.Retries,
		&state.HeartbeatAtMS, &state.LastProgressAtMS, &state.LastSuccessAtMS, &state.LastError)
	return state, err
}

func nullableString(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return string(value)
}

func SortMutations(mutations []Mutation) {
	sort.Slice(mutations, func(i, j int) bool { return mutations[i].Sequence < mutations[j].Sequence })
}
