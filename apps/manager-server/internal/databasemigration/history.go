package databasemigration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

type ColumnSpec struct {
	Name        string  `json:"name"`
	Nullable    bool    `json:"nullable"`
	LogicalType string  `json:"logicalType"`
	DefaultSQL  *string `json:"defaultSql,omitempty"`
}

type TableSpec struct {
	Name          string       `json:"name"`
	Columns       []ColumnSpec `json:"columns"`
	PrimaryKey    []string     `json:"primaryKey"`
	Dependencies  []string     `json:"dependencies,omitempty"`
	VersionColumn string       `json:"versionColumn,omitempty"`
	Derived       bool         `json:"derived"`
}

type Manifest interface {
	Tables() []TableSpec
}

type StaticManifest []TableSpec

func (m StaticManifest) Tables() []TableSpec { return slices.Clone(m) }

func ValidateManifest(manifest Manifest) error {
	if manifest == nil {
		return errors.New("migration manifest is required")
	}
	tables := manifest.Tables()
	known := make(map[string]TableSpec, len(tables))
	for _, table := range tables {
		if table.Name == "" || len(table.Columns) == 0 || len(table.PrimaryKey) == 0 {
			return fmt.Errorf("table manifest requires name, columns, and primary key: %#v", table)
		}
		if _, exists := known[table.Name]; exists {
			return fmt.Errorf("duplicate manifest table %q", table.Name)
		}
		columns := make(map[string]struct{}, len(table.Columns))
		for _, column := range table.Columns {
			if column.Name == "" || column.LogicalType == "" {
				return fmt.Errorf("table %q has a column without name or logical type", table.Name)
			}
			if _, exists := columns[column.Name]; exists {
				return fmt.Errorf("table %q has duplicate column %q", table.Name, column.Name)
			}
			columns[column.Name] = struct{}{}
		}
		for _, key := range table.PrimaryKey {
			if _, exists := columns[key]; !exists {
				return fmt.Errorf("table %q primary key column %q is missing", table.Name, key)
			}
		}
		if table.VersionColumn != "" {
			if _, exists := columns[table.VersionColumn]; !exists {
				return fmt.Errorf("table %q version column %q is missing", table.Name, table.VersionColumn)
			}
		}
		known[table.Name] = table
	}
	for _, table := range tables {
		for _, dependency := range table.Dependencies {
			if _, exists := known[dependency]; !exists {
				return fmt.Errorf("table %q has unknown dependency %q", table.Name, dependency)
			}
		}
	}
	_, err := OrderedTables(manifest)
	return err
}

func OrderedTables(manifest Manifest) ([]TableSpec, error) {
	tables := manifest.Tables()
	byName := make(map[string]TableSpec, len(tables))
	for _, table := range tables {
		byName[table.Name] = table
	}
	result := make([]TableSpec, 0, len(tables))
	visiting := make(map[string]bool, len(tables))
	visited := make(map[string]bool, len(tables))
	var visit func(string) error
	visit = func(name string) error {
		if visiting[name] {
			return fmt.Errorf("manifest dependency cycle includes %q", name)
		}
		if visited[name] {
			return nil
		}
		table, exists := byName[name]
		if !exists {
			return fmt.Errorf("unknown manifest table %q", name)
		}
		visiting[name] = true
		for _, dependency := range table.Dependencies {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		visiting[name] = false
		visited[name] = true
		result = append(result, table)
		return nil
	}
	for _, table := range tables {
		if err := visit(table.Name); err != nil {
			return nil, err
		}
	}
	return result, nil
}

type AuthoritativeRow struct {
	Values     []any
	PrimaryKey json.RawMessage
	RowVersion int64
	Bytes      int64
}

type HistoryBatch struct {
	Rows           []AuthoritativeRow
	NextCheckpoint json.RawMessage
	Done           bool
}

type HistorySource interface {
	CaptureWatermark(ctx context.Context, table TableSpec) (json.RawMessage, error)
	ReadBatch(ctx context.Context, table TableSpec, checkpoint, sourceWatermark json.RawMessage, limit int) (HistoryBatch, error)
}

type HistoryTarget interface {
	// ApplyBatch must apply the complete batch in one target transaction. It
	// must preserve explicit primary keys and ignore an older RowVersion when a
	// newer real-time Outbox mutation already exists.
	ApplyBatch(ctx context.Context, table TableSpec, sourceWatermark json.RawMessage, rows []AuthoritativeRow) error
}

type HistoryRowApplier func(ctx context.Context, tx *sql.Tx, table TableSpec, row AuthoritativeRow) error

// SQLHistoryTarget is the reusable target implementation for a manifest
// batch. It wraps all rows in one target transaction and uses the same durable
// row-version table as real-time Inbox application.
type SQLHistoryTarget struct {
	Repository  *SQLRepository
	Namespace   string
	SourceEpoch int64
	ApplyRow    HistoryRowApplier
}

func (target SQLHistoryTarget) ApplyBatch(ctx context.Context, table TableSpec, _ json.RawMessage, rows []AuthoritativeRow) error {
	if target.Repository == nil || target.Repository.db == nil || target.Namespace == "" ||
		target.SourceEpoch <= 0 || target.ApplyRow == nil {
		return errors.New("sql history target requires repository, namespace, source epoch, and row applier")
	}
	if err := validateHistoryBatch(table, HistoryBatch{Rows: rows, NextCheckpoint: json.RawMessage(`true`), Done: true}); err != nil {
		return err
	}
	tx, err := target.Repository.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, row := range rows {
		encoded, err := json.Marshal(struct {
			Table      string          `json:"table"`
			PrimaryKey json.RawMessage `json:"primaryKey"`
			RowVersion int64           `json:"rowVersion"`
			Values     []any           `json:"values"`
		}{Table: table.Name, PrimaryKey: row.PrimaryKey, RowVersion: row.RowVersion, Values: row.Values})
		if err != nil {
			return fmt.Errorf("encode historical row digest: %w", err)
		}
		digest := sha256.Sum256(encoded)
		idInput := append([]byte(target.Namespace+"\x00"+table.Name+"\x00"), row.PrimaryKey...)
		idInput = append(idInput, []byte(fmt.Sprintf("\x00%d\x00%d", target.SourceEpoch, row.RowVersion))...)
		idDigest := sha256.Sum256(idInput)
		accepted, err := target.Repository.AcceptHistoricalRowVersion(ctx, tx, RowVersionRecord{
			Table: table.Name, PrimaryKey: row.PrimaryKey, SourceEpoch: target.SourceEpoch,
			RowVersion: row.RowVersion, MutationID: "history-" + hex.EncodeToString(idDigest[:]),
			MutationHash: hex.EncodeToString(digest[:]),
		})
		if err != nil {
			return err
		}
		if accepted {
			if err := target.ApplyRow(ctx, tx, table, row); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

type ProgressStore interface {
	Migration(ctx context.Context, id string) (Migration, error)
	TableProgress(ctx context.Context, migrationID, table string) (TableProgress, bool, error)
	InitializeTableProgress(ctx context.Context, migrationID, table string, expectedGeneration int64, sourceWatermark json.RawMessage, batchSize int) (Migration, TableProgress, error)
	SaveTableProgress(ctx context.Context, migrationID string, expectedGeneration int64, progress TableProgress) (Migration, error)
}

type HistoryCopier struct {
	Manifest      Manifest
	Source        HistorySource
	Target        HistoryTarget
	Progress      ProgressStore
	MaxBatchBytes int64
}

type CopyBatchResult struct {
	Migration Migration
	Progress  TableProgress
	Rows      int
	Done      bool
}

func (c *HistoryCopier) RunBatch(ctx context.Context, migrationID, tableName string, expectedGeneration int64) (CopyBatchResult, error) {
	if err := ValidateManifest(c.Manifest); err != nil {
		return CopyBatchResult{}, err
	}
	if c.Source == nil || c.Target == nil || c.Progress == nil {
		return CopyBatchResult{}, errors.New("history copier requires source, target, and progress store")
	}
	migration, err := c.Progress.Migration(ctx, migrationID)
	if err != nil {
		return CopyBatchResult{}, err
	}
	if migration.Generation != expectedGeneration {
		return CopyBatchResult{}, &ConflictError{Kind: ErrGenerationConflict, Expected: expectedGeneration, Actual: migration.Generation}
	}
	if migration.Phase != PhaseCopyHistory || migration.Status != StatusRunning {
		return CopyBatchResult{}, fmt.Errorf("%w: history copy requires a running copy_history phase", ErrInvalidTransition)
	}
	table, found := findTable(c.Manifest, tableName)
	if !found || table.Derived {
		return CopyBatchResult{}, fmt.Errorf("authoritative manifest table %q not found", tableName)
	}
	for _, dependency := range table.Dependencies {
		state, exists, err := c.Progress.TableProgress(ctx, migrationID, dependency)
		if err != nil {
			return CopyBatchResult{}, err
		}
		if !exists || !state.Completed {
			return CopyBatchResult{}, fmt.Errorf("%w: dependency %q is incomplete", ErrInvalidTransition, dependency)
		}
	}
	progress, exists, err := c.Progress.TableProgress(ctx, migrationID, tableName)
	if err != nil {
		return CopyBatchResult{}, err
	}
	if !exists {
		watermark, err := c.Source.CaptureWatermark(ctx, table)
		if err != nil {
			return CopyBatchResult{}, err
		}
		migration, progress, err = c.Progress.InitializeTableProgress(ctx, migrationID, tableName,
			expectedGeneration, watermark, migration.BatchSize)
		return CopyBatchResult{Migration: migration, Progress: progress}, err
	}
	if progress.Completed {
		return CopyBatchResult{Migration: migration, Progress: progress, Done: true}, nil
	}
	limit := progress.BatchSize
	if limit <= 0 {
		limit = migration.BatchSize
	}
	if limit <= 0 {
		limit = DefaultBatchSize
	}
	batch, err := c.Source.ReadBatch(ctx, table, progress.Checkpoint, progress.SourceWatermark, limit)
	if err != nil {
		return CopyBatchResult{}, err
	}
	if err := validateHistoryBatch(table, batch); err != nil {
		return CopyBatchResult{}, err
	}
	if len(batch.Rows) > limit {
		return CopyBatchResult{}, fmt.Errorf("history source returned %d rows for limit %d", len(batch.Rows), limit)
	}
	if len(batch.Rows) > 0 {
		if err := c.Target.ApplyBatch(ctx, table, progress.SourceWatermark, batch.Rows); err != nil {
			return CopyBatchResult{}, err
		}
	}
	batchBytes := int64(0)
	for _, row := range batch.Rows {
		batchBytes += row.Bytes
	}
	progress.Checkpoint = cloneRaw(batch.NextCheckpoint)
	progress.RowsCopied += int64(len(batch.Rows))
	progress.BytesCopied += batchBytes
	progress.Requests++
	progress.Completed = batch.Done
	maxBytes := c.MaxBatchBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBatchBytes
	}
	progress.BatchSize = AdaptBatchSize(limit, len(batch.Rows), batchBytes, maxBytes)
	migration, err = c.Progress.SaveTableProgress(ctx, migrationID, expectedGeneration, progress)
	if err != nil {
		return CopyBatchResult{}, err
	}
	return CopyBatchResult{Migration: migration, Progress: progress, Rows: len(batch.Rows), Done: progress.Completed}, nil
}

func AdaptBatchSize(current, rows int, bytes, maxBytes int64) int {
	if current <= 0 {
		current = DefaultBatchSize
	}
	if rows <= 0 || bytes <= 0 || maxBytes <= 0 {
		return current
	}
	average := (bytes + int64(rows) - 1) / int64(rows)
	limit := int(maxBytes / average)
	if limit < 1 {
		limit = 1
	}
	if limit > DefaultBatchSize {
		limit = DefaultBatchSize
	}
	if limit < current {
		return limit
	}
	// Grow conservatively after small batches; shrinking oversized batches is
	// immediate, while growth is bounded to avoid memory oscillation.
	if bytes < maxBytes/2 && current < limit {
		grown := current + current/4
		if grown <= current {
			grown = current + 1
		}
		return min(grown, limit)
	}
	return current
}

func validateHistoryBatch(table TableSpec, batch HistoryBatch) error {
	if len(batch.Rows) == 0 && !batch.Done {
		return errors.New("history source returned an empty non-terminal batch")
	}
	if len(batch.Rows) > 0 && len(batch.NextCheckpoint) == 0 {
		return errors.New("history source did not return the next checkpoint")
	}
	keyIndexes := make([]int, 0, len(table.PrimaryKey))
	for _, key := range table.PrimaryKey {
		for index, column := range table.Columns {
			if column.Name == key {
				keyIndexes = append(keyIndexes, index)
				break
			}
		}
	}
	for index, row := range batch.Rows {
		if len(row.Values) != len(table.Columns) {
			return fmt.Errorf("table %q row %d has %d values, want all %d manifest columns", table.Name, index, len(row.Values), len(table.Columns))
		}
		if len(row.PrimaryKey) == 0 || bytes.Equal(bytes.TrimSpace(row.PrimaryKey), []byte("null")) {
			return fmt.Errorf("table %q row %d does not preserve an explicit primary key", table.Name, index)
		}
		for _, keyIndex := range keyIndexes {
			if row.Values[keyIndex] == nil {
				return fmt.Errorf("table %q row %d has a null primary key", table.Name, index)
			}
		}
		if row.Bytes < 0 {
			return fmt.Errorf("table %q row %d has negative size", table.Name, index)
		}
	}
	return nil
}

func findTable(manifest Manifest, name string) (TableSpec, bool) {
	for _, table := range manifest.Tables() {
		if table.Name == name {
			return table, true
		}
	}
	return TableSpec{}, false
}

func cloneRaw(value json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}

func (r *SQLRepository) TableProgress(ctx context.Context, migrationID, table string) (TableProgress, bool, error) {
	progress, err := scanTableProgress(r.db.QueryRowContext(ctx, `SELECT migration_id, table_name,
		checkpoint_json, source_watermark_json, rows_copied, bytes_copied, requests, batch_size,
		completed, updated_at_ms FROM database_migration_tables WHERE migration_id = ? AND table_name = ?`, migrationID, table))
	if errors.Is(err, sql.ErrNoRows) {
		return TableProgress{}, false, nil
	}
	return progress, err == nil, err
}

func (r *SQLRepository) ListTableProgress(ctx context.Context, migrationID string) ([]TableProgress, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT migration_id, table_name,
		checkpoint_json, source_watermark_json, rows_copied, bytes_copied, requests, batch_size,
		completed, updated_at_ms FROM database_migration_tables
		WHERE migration_id = ? ORDER BY table_name`, migrationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	progress := make([]TableProgress, 0)
	for rows.Next() {
		item, scanErr := scanTableProgress(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		progress = append(progress, item)
	}
	return progress, rows.Err()
}

func (r *SQLRepository) InitializeTableProgress(ctx context.Context, migrationID, table string, expectedGeneration int64, sourceWatermark json.RawMessage, batchSize int) (Migration, TableProgress, error) {
	if table == "" || len(sourceWatermark) == 0 {
		return Migration{}, TableProgress{}, errors.New("table progress requires a table and source watermark")
	}
	if batchSize <= 0 {
		batchSize = DefaultBatchSize
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Migration{}, TableProgress{}, err
	}
	defer tx.Rollback()
	migration, err := scanMigration(tx.QueryRowContext(ctx, migrationSelect+` WHERE id = ?`, migrationID))
	if err != nil {
		return Migration{}, TableProgress{}, err
	}
	if migration.Generation != expectedGeneration {
		return Migration{}, TableProgress{}, &ConflictError{Kind: ErrGenerationConflict, Expected: expectedGeneration, Actual: migration.Generation}
	}
	existing, err := scanTableProgress(tx.QueryRowContext(ctx, `SELECT migration_id, table_name,
		checkpoint_json, source_watermark_json, rows_copied, bytes_copied, requests, batch_size,
		completed, updated_at_ms FROM database_migration_tables WHERE migration_id = ? AND table_name = ?`, migrationID, table))
	if err == nil {
		return migration, existing, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Migration{}, TableProgress{}, err
	}
	nowMS := r.now().UnixMilli()
	progress := TableProgress{MigrationID: migrationID, Table: table, SourceWatermark: cloneRaw(sourceWatermark), BatchSize: batchSize, UpdatedAtMS: nowMS}
	if _, err := tx.ExecContext(ctx, `INSERT INTO database_migration_tables (migration_id,
		table_name, checkpoint_json, source_watermark_json, rows_copied, bytes_copied,
		requests, batch_size, completed, updated_at_ms) VALUES (?, ?, NULL, ?, 0, 0, 0, ?, 0, ?)`,
		migrationID, table, string(sourceWatermark), batchSize, nowMS); err != nil {
		return Migration{}, TableProgress{}, err
	}
	migration.Generation++
	migration.UpdatedAtMS = nowMS
	if _, err := tx.ExecContext(ctx, `UPDATE database_migrations SET generation = ?, updated_at_ms = ?
		WHERE id = ? AND generation = ?`, migration.Generation, nowMS, migration.ID, expectedGeneration); err != nil {
		return Migration{}, TableProgress{}, err
	}
	if err := appendMigrationTableEvent(ctx, tx, migration, "table_started", table); err != nil {
		return Migration{}, TableProgress{}, err
	}
	if err := tx.Commit(); err != nil {
		return Migration{}, TableProgress{}, err
	}
	return migration, progress, nil
}

func (r *SQLRepository) SaveTableProgress(ctx context.Context, migrationID string, expectedGeneration int64, progress TableProgress) (Migration, error) {
	if progress.MigrationID != "" && progress.MigrationID != migrationID {
		return Migration{}, errors.New("table progress belongs to another migration")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Migration{}, err
	}
	defer tx.Rollback()
	migration, err := scanMigration(tx.QueryRowContext(ctx, migrationSelect+` WHERE id = ?`, migrationID))
	if err != nil {
		return Migration{}, err
	}
	if migration.Generation != expectedGeneration {
		return Migration{}, &ConflictError{Kind: ErrGenerationConflict, Expected: expectedGeneration, Actual: migration.Generation}
	}
	current, err := scanTableProgress(tx.QueryRowContext(ctx, `SELECT migration_id, table_name,
		checkpoint_json, source_watermark_json, rows_copied, bytes_copied, requests, batch_size,
		completed, updated_at_ms FROM database_migration_tables WHERE migration_id = ? AND table_name = ?`, migrationID, progress.Table))
	if err != nil {
		return Migration{}, err
	}
	if current.Completed && !progress.Completed || progress.RowsCopied < current.RowsCopied ||
		progress.BytesCopied < current.BytesCopied || progress.Requests < current.Requests ||
		!bytes.Equal(current.SourceWatermark, progress.SourceWatermark) {
		return Migration{}, errors.New("table progress cannot regress or change its source watermark")
	}
	nowMS := r.now().UnixMilli()
	var checkpoint any
	if len(progress.Checkpoint) > 0 {
		checkpoint = string(progress.Checkpoint)
	}
	result, err := tx.ExecContext(ctx, `UPDATE database_migration_tables SET checkpoint_json = ?,
		rows_copied = ?, bytes_copied = ?, requests = ?, batch_size = ?, completed = ?,
		updated_at_ms = ? WHERE migration_id = ? AND table_name = ?`, checkpoint, progress.RowsCopied,
		progress.BytesCopied, progress.Requests, progress.BatchSize, boolInt(progress.Completed), nowMS,
		migrationID, progress.Table)
	if err != nil {
		return Migration{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return Migration{}, ErrNotFound
	}
	migration.Generation++
	migration.UpdatedAtMS = nowMS
	result, err = tx.ExecContext(ctx, `UPDATE database_migrations SET generation = ?, updated_at_ms = ?
		WHERE id = ? AND generation = ?`, migration.Generation, nowMS, migration.ID, expectedGeneration)
	if err != nil {
		return Migration{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return Migration{}, ErrGenerationConflict
	}
	if !current.Completed && progress.Completed {
		if err := appendMigrationTableEvent(ctx, tx, migration, "table_completed", progress.Table); err != nil {
			return Migration{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Migration{}, err
	}
	return migration, nil
}

func scanTableProgress(row interface{ Scan(...any) error }) (TableProgress, error) {
	var progress TableProgress
	var checkpoint, watermark sql.NullString
	var completed int
	err := row.Scan(&progress.MigrationID, &progress.Table, &checkpoint, &watermark,
		&progress.RowsCopied, &progress.BytesCopied, &progress.Requests, &progress.BatchSize,
		&completed, &progress.UpdatedAtMS)
	if err != nil {
		return TableProgress{}, err
	}
	if checkpoint.Valid {
		progress.Checkpoint = json.RawMessage(checkpoint.String)
	}
	if watermark.Valid {
		progress.SourceWatermark = json.RawMessage(watermark.String)
	}
	progress.Completed = completed != 0
	return progress, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
