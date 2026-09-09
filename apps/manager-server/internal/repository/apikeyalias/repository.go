package apikeyalias

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/outboxcontext"
	sqldialect "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/dialect"
)

type Repository interface {
	LoadAll(ctx context.Context) ([]model.APIKeyAlias, error)
	UpsertMany(ctx context.Context, aliases []model.APIKeyAlias, activeHashes []string, allowOrphanCleanup bool) error
	Delete(ctx context.Context, apiKeyHash string) error
}

type repository struct {
	db      *sql.DB
	dialect sqldialect.Dialect
}

func New(db *sql.DB) Repository {
	return NewForBackend(db, database.BackendSQLite)
}

func NewForBackend(db *sql.DB, backend database.BackendKind) Repository {
	return &repository{db: db, dialect: sqldialect.ForBackend(backend)}
}

func (r *repository) LoadAll(ctx context.Context) ([]model.APIKeyAlias, error) {
	rows, err := r.db.QueryContext(ctx, `select api_key_hash, alias, updated_at_ms
		from api_key_aliases`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	aliases := []model.APIKeyAlias{}
	for rows.Next() {
		var alias model.APIKeyAlias
		if err := rows.Scan(&alias.APIKeyHash, &alias.Alias, &alias.UpdatedAtMS); err != nil {
			return nil, err
		}
		aliases = append(aliases, alias)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(aliases, func(i, j int) bool {
		left := apiKeyAliasSortKey(aliases[i].Alias)
		right := apiKeyAliasSortKey(aliases[j].Alias)
		if left != right {
			return left < right
		}
		return aliases[i].APIKeyHash < aliases[j].APIKeyHash
	})
	return aliases, nil
}

func (r *repository) UpsertMany(ctx context.Context, aliases []model.APIKeyAlias, activeHashes []string, allowOrphanCleanup bool) error {
	if len(aliases) == 0 {
		return nil
	}
	now := time.Now().UnixMilli()
	normalizedAliases := make([]model.APIKeyAlias, 0, len(aliases))
	seenAliases := map[string]string{}
	for _, alias := range aliases {
		normalized, err := normalizeAPIKeyAlias(alias, now)
		if err != nil {
			return err
		}
		aliasKey := normalizeAPIKeyAliasUniqueKey(normalized.Alias)
		if existingHash, ok := seenAliases[aliasKey]; ok && existingHash != normalized.APIKeyHash {
			return errors.New("api key alias already exists")
		}
		seenAliases[aliasKey] = normalized.APIKeyHash
		normalizedAliases = append(normalizedAliases, normalized)
	}

	var activeSet map[string]struct{}
	if len(activeHashes) > 0 {
		activeSet = make(map[string]struct{}, len(activeHashes)+len(normalizedAliases))
		for _, h := range activeHashes {
			hash := strings.ToLower(strings.TrimSpace(h))
			if validAPIKeyHash(hash) {
				activeSet[hash] = struct{}{}
			}
		}
		for _, normalized := range normalizedAliases {
			activeSet[normalized.APIKeyHash] = struct{}{}
		}
	}

	var txOptions *sql.TxOptions
	if r.dialect.IsMySQL() {
		// Alias uniqueness is defined by Go's Unicode normalization rather
		// than a lossy indexed prefix. SERIALIZABLE plus the full FOR UPDATE
		// scan prevents two writers from claiming the same normalized alias.
		txOptions = &sql.TxOptions{Isolation: sql.LevelSerializable}
	}
	tx, err := outboxcontext.Begin(ctx, r.db, txOptions)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	stmt, err := tx.PrepareContext(ctx, `insert into api_key_aliases (
		api_key_hash, alias, updated_at_ms
	) values (?, ?, ?)`+r.dialect.UpsertClause(
		[]string{"api_key_hash"},
		[]string{"alias", "updated_at_ms"},
	))
	if err != nil {
		return err
	}
	defer stmt.Close()

	deleteStmt, err := tx.PrepareContext(ctx, `delete from api_key_aliases where api_key_hash = ?`)
	if err != nil {
		return err
	}
	defer deleteStmt.Close()

	existingRows, err := tx.QueryContext(ctx, `select api_key_hash, alias from api_key_aliases`+r.dialect.ForUpdate())
	if err != nil {
		return err
	}
	existingAliases := map[string]string{}
	for existingRows.Next() {
		var apiKeyHash string
		var alias string
		if err := existingRows.Scan(&apiKeyHash, &alias); err != nil {
			_ = existingRows.Close()
			return err
		}
		existingAliases[normalizeAPIKeyAliasUniqueKey(alias)] = apiKeyHash
	}
	if err := existingRows.Close(); err != nil {
		return err
	}
	if err := existingRows.Err(); err != nil {
		return err
	}

	for _, normalized := range normalizedAliases {
		aliasKey := normalizeAPIKeyAliasUniqueKey(normalized.Alias)
		if existingHash, ok := existingAliases[aliasKey]; ok && existingHash != normalized.APIKeyHash {
			if activeSet == nil {
				return errors.New("api key alias already exists")
			}
			if _, isActive := activeSet[existingHash]; isActive {
				return errors.New("api key alias already exists")
			}
			if !allowOrphanCleanup {
				return errors.New("api key alias already exists")
			}
			if _, err := deleteStmt.ExecContext(ctx, existingHash); err != nil {
				return err
			}
			delete(existingAliases, aliasKey)
		}
		if _, err := stmt.ExecContext(
			ctx,
			normalized.APIKeyHash,
			normalized.Alias,
			normalized.UpdatedAtMS,
		); err != nil {
			return err
		}
		existingAliases[aliasKey] = normalized.APIKeyHash
	}
	return tx.Commit()
}

func (r *repository) Delete(ctx context.Context, apiKeyHash string) error {
	hash := strings.ToLower(strings.TrimSpace(apiKeyHash))
	if !validAPIKeyHash(hash) {
		return errors.New("valid apiKeyHash is required")
	}
	_, err := outboxcontext.Exec(ctx, r.db, `delete from api_key_aliases where api_key_hash = ?`, hash)
	return err
}

func normalizeAPIKeyAlias(alias model.APIKeyAlias, now int64) (model.APIKeyAlias, error) {
	hash := strings.ToLower(strings.TrimSpace(alias.APIKeyHash))
	if !validAPIKeyHash(hash) {
		return model.APIKeyAlias{}, errors.New("valid apiKeyHash is required")
	}
	label := strings.TrimSpace(alias.Alias)
	if label == "" {
		return model.APIKeyAlias{}, errors.New("alias is required")
	}
	if len([]rune(label)) > 120 {
		return model.APIKeyAlias{}, errors.New("alias must be 120 characters or less")
	}
	if alias.UpdatedAtMS <= 0 {
		alias.UpdatedAtMS = now
	}
	alias.APIKeyHash = hash
	alias.Alias = label
	return alias, nil
}

func normalizeAPIKeyAliasUniqueKey(alias string) string {
	return strings.ToLower(strings.TrimSpace(alias))
}

// apiKeyAliasSortKey reproduces SQLite NOCASE ordering, which folds ASCII
// letters only. Sorting in Go gives MySQL the same stable order without
// depending on a server collation and preserves SQLite's existing behavior.
func apiKeyAliasSortKey(alias string) string {
	value := []byte(alias)
	for index, char := range value {
		if char >= 'A' && char <= 'Z' {
			value[index] = char + ('a' - 'A')
		}
	}
	return string(value)
}

func validAPIKeyHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') {
			continue
		}
		return false
	}
	return true
}
