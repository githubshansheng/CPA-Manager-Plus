package mysql

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
)

type Capability struct {
	Available bool   `json:"available"`
	Error     string `json:"error,omitempty"`
}

type TestResult struct {
	Endpoint               string                `json:"endpoint"`
	Database               string                `json:"database"`
	Username               string                `json:"username"`
	Version                string                `json:"version"`
	VersionComment         string                `json:"versionComment,omitempty"`
	RequiredCollation      string                `json:"requiredCollation"`
	CharacterSet           string                `json:"characterSet"`
	Collation              string                `json:"collation"`
	ConnectionCharacterSet string                `json:"connectionCharacterSet"`
	ConnectionCollation    string                `json:"connectionCollation"`
	TimeZone               string                `json:"timeZone"`
	SQLMode                string                `json:"sqlMode"`
	InnoDBStrictMode       bool                  `json:"innodbStrictMode"`
	TLSMode                string                `json:"tlsMode"`
	TLSCipher              string                `json:"tlsCipher,omitempty"`
	PingLatencyMS          int64                 `json:"pingLatencyMs"`
	MaxAllowedPacket       int64                 `json:"maxAllowedPacket"`
	Capabilities           map[string]Capability `json:"capabilities"`
}

// RequireMigrationCapabilities turns the diagnostic probe into the safety
// gate used before persisting a migration target or creating its schema.
func (r TestResult) RequireMigrationCapabilities() error {
	missing := make([]string, 0, 3)
	for _, name := range []string{"read", "write", "ddl"} {
		capability, ok := r.Capabilities[name]
		if !ok || !capability.Available {
			reason := capability.Error
			if reason == "" {
				reason = "probe did not report success"
			}
			missing = append(missing, name+": "+reason)
		}
	}
	if len(missing) > 0 {
		return errors.New("mysql migration permissions are incomplete: " + strings.Join(missing, "; "))
	}
	return nil
}

func Test(ctx context.Context, config Config) (TestResult, error) {
	config = config.withDefaults()
	result := TestResult{
		Endpoint: config.MaskedEndpoint(), Database: config.Database, Username: config.Username,
		TLSMode: config.TLSMode, Capabilities: map[string]Capability{},
	}
	started := time.Now()
	db, err := openDiagnostic(ctx, config)
	result.PingLatencyMS = time.Since(started).Milliseconds()
	if err != nil {
		return result, err
	}
	defer db.Close()
	if err := db.QueryRowContext(ctx, `select @@version, @@version_comment`).Scan(
		&result.Version, &result.VersionComment); err != nil {
		return result, fmt.Errorf("inspect mysql server version: %w", err)
	}
	serverVersion, err := database.ParseSupportedMySQLVersion(result.Version, result.VersionComment)
	if err != nil {
		return result, err
	}
	result.RequiredCollation = serverVersion.StorageCollation()
	if _, err := db.ExecContext(ctx, "SET SESSION collation_connection="+
		result.RequiredCollation); err != nil {
		return result, fmt.Errorf("set required mysql session collation %s: %w",
			result.RequiredCollation, err)
	}
	var innodbStrict int
	if err := db.QueryRowContext(ctx, `select @@character_set_database,
		@@collation_database, @@character_set_connection,
		@@collation_connection, @@session.time_zone, @@session.sql_mode,
		@@session.innodb_strict_mode, @@global.max_allowed_packet`).Scan(
		&result.CharacterSet, &result.Collation, &result.ConnectionCharacterSet, &result.ConnectionCollation,
		&result.TimeZone, &result.SQLMode, &innodbStrict, &result.MaxAllowedPacket,
	); err != nil {
		return result, fmt.Errorf("inspect mysql server: %w", err)
	}
	result.InnoDBStrictMode = innodbStrict != 0
	if err := validateServerSettings(result, result.RequiredCollation); err != nil {
		return result, err
	}
	result.TLSCipher, _ = statusString(ctx, db, "Ssl_cipher")
	if config.TLSMode == TLSVerifyIdentity && result.TLSCipher == "" {
		return result, errors.New("mysql connection did not negotiate TLS")
	}
	result.Capabilities = inspectCapabilities(ctx, db, config.Database, result.RequiredCollation)
	return result, nil
}

const minimumMaxAllowedPacket = int64(4 << 20)

func validateServerSettings(result TestResult, requiredCollation string) error {
	// The database default is diagnostic only. Every CPAMP table declares its
	// own version-specific utf8mb4 collation, so requiring an empty database to
	// be created with that default would prevent saving the connection before
	// the administrator can run schema initialization.
	if result.ConnectionCharacterSet != database.MySQLCharacterSet ||
		result.ConnectionCollation != requiredCollation {
		return fmt.Errorf("mysql connection character set/collation is %s/%s, want %s/%s",
			result.ConnectionCharacterSet, result.ConnectionCollation,
			database.MySQLCharacterSet, requiredCollation)
	}
	if result.TimeZone != "+00:00" && !strings.EqualFold(result.TimeZone, "UTC") {
		return fmt.Errorf("mysql session time zone is %q, want +00:00", result.TimeZone)
	}
	modes := "," + strings.ToUpper(result.SQLMode) + ","
	if !strings.Contains(modes, ",STRICT_TRANS_TABLES,") &&
		!strings.Contains(modes, ",STRICT_ALL_TABLES,") {
		return fmt.Errorf("mysql sql_mode %q has no strict table mode", result.SQLMode)
	}
	if !result.InnoDBStrictMode {
		return errors.New("mysql innodb_strict_mode must be enabled")
	}
	if result.MaxAllowedPacket < minimumMaxAllowedPacket {
		return fmt.Errorf("mysql max_allowed_packet is %d, require at least %d",
			result.MaxAllowedPacket, minimumMaxAllowedPacket)
	}
	return nil
}

func validateServerVersion(version, comment string) error {
	_, err := database.ParseSupportedMySQLVersion(version, comment)
	return err
}

func inspectCapabilities(ctx context.Context, db *sql.DB, database, requiredCollation string) map[string]Capability {
	result := map[string]Capability{
		"read":  {Error: "target-schema SELECT probe did not complete"},
		"write": {Error: "target-schema DML probe did not complete"},
		"ddl":   {Error: "target-schema DDL probe did not complete"},
	}
	probeName, err := capabilityProbeName()
	if err != nil {
		message := "create permission probe identity: " + err.Error()
		for name := range result {
			result[name] = Capability{Error: message}
		}
		return result
	}
	qualified := quoteMySQLIdentifier(database) + "." + quoteMySQLIdentifier(probeName)
	childName := probeName + "_child"
	childQualified := quoteMySQLIdentifier(database) + "." + quoteMySQLIdentifier(childName)
	if _, err := db.ExecContext(ctx, "CREATE TABLE "+qualified+
		" (`id` BIGINT NOT NULL PRIMARY KEY, `value` LONGTEXT NULL) ENGINE=InnoDB "+
		"DEFAULT CHARACTER SET utf8mb4 COLLATE "+requiredCollation); err != nil {
		message := "CREATE probe failed: " + err.Error()
		result["ddl"] = Capability{Error: message}
		result["read"] = Capability{Error: "target-schema SELECT was not tested because CREATE failed"}
		result["write"] = Capability{Error: "target-schema DML was not tested because CREATE failed"}
		return result
	}

	ddlErrors := make([]string, 0, 7)
	if _, err := db.ExecContext(ctx, "ALTER TABLE "+qualified+
		" ADD COLUMN `marker` VARCHAR(32) NULL"); err != nil {
		ddlErrors = append(ddlErrors, "ALTER: "+err.Error())
	}
	if _, err := db.ExecContext(ctx, "CREATE INDEX "+quoteMySQLIdentifier(probeName+"_idx")+
		" ON "+qualified+" (`marker`)"); err != nil {
		ddlErrors = append(ddlErrors, "INDEX: "+err.Error())
	}
	childCreated := false
	if _, err := db.ExecContext(ctx, "CREATE TABLE "+childQualified+
		" (`id` BIGINT NOT NULL PRIMARY KEY, `parent_id` BIGINT NOT NULL, "+
		"CONSTRAINT "+quoteMySQLIdentifier(probeName+"_fk")+" FOREIGN KEY (`parent_id`) REFERENCES "+
		qualified+" (`id`)) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE "+requiredCollation); err != nil {
		ddlErrors = append(ddlErrors, capabilityProbeError("REFERENCES (CREATE FOREIGN KEY)", err))
	} else {
		childCreated = true
	}
	triggerName := quoteMySQLIdentifier(database) + "." + quoteMySQLIdentifier(probeName+"_trg")
	triggerCreated := false
	if _, err := db.ExecContext(ctx, "CREATE TRIGGER "+triggerName+" BEFORE INSERT ON "+qualified+
		" FOR EACH ROW SET NEW.`value`=NEW.`value`"); err != nil {
		ddlErrors = append(ddlErrors, capabilityProbeError("CREATE TRIGGER", err))
	} else {
		triggerCreated = true
	}
	if triggerCreated {
		if _, err := db.ExecContext(ctx, "DROP TRIGGER "+triggerName); err != nil {
			ddlErrors = append(ddlErrors, "DROP TRIGGER: "+err.Error())
		}
	}

	writeErrors := make([]string, 0, 3)
	if _, err := db.ExecContext(ctx, "INSERT INTO "+qualified+" (`id`,`value`) VALUES (1,'probe')"); err != nil {
		writeErrors = append(writeErrors, "INSERT: "+err.Error())
	}
	if _, err := db.ExecContext(ctx, "UPDATE "+qualified+" SET `value`='updated' WHERE `id`=1"); err != nil {
		writeErrors = append(writeErrors, "UPDATE: "+err.Error())
	}
	var value string
	if err := db.QueryRowContext(ctx, "SELECT `value` FROM "+qualified+" WHERE `id`=1").Scan(&value); err != nil {
		result["read"] = Capability{Error: "SELECT probe failed: " + err.Error()}
	} else if value != "updated" {
		result["read"] = Capability{Error: "SELECT probe returned unexpected data"}
	} else {
		result["read"] = Capability{Available: true}
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM "+qualified+" WHERE `id`=1"); err != nil {
		writeErrors = append(writeErrors, "DELETE: "+err.Error())
	}
	if len(writeErrors) == 0 {
		result["write"] = Capability{Available: true}
	} else {
		result["write"] = Capability{Error: strings.Join(writeErrors, "; ")}
	}

	if childCreated {
		if _, err := db.ExecContext(ctx, "DROP TABLE "+childQualified); err != nil {
			ddlErrors = append(ddlErrors, "DROP child cleanup: "+err.Error())
		}
	}
	if _, err := db.ExecContext(ctx, "DROP TABLE "+qualified); err != nil {
		ddlErrors = append(ddlErrors, "DROP cleanup: "+err.Error())
	}
	if len(ddlErrors) == 0 {
		result["ddl"] = Capability{Available: true}
	} else {
		result["ddl"] = Capability{Error: strings.Join(ddlErrors, "; ")}
	}
	return result
}

func capabilityProbeError(action string, err error) string {
	var mysqlErr *drivermysql.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 1419 {
		return action + ": MySQL Error 1419: binary logging blocks trigger creation; " +
			"ask a DBA to persist log_bin_trust_function_creators=ON; " +
			"do not grant SUPER to the application account"
	}
	return action + ": " + err.Error()
}

func capabilityProbeName() (string, error) {
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return "__cpamp_permission_probe_" + hex.EncodeToString(random), nil
}

func quoteMySQLIdentifier(identifier string) string {
	return "`" + strings.ReplaceAll(identifier, "`", "``") + "`"
}

func statusString(ctx context.Context, db *sql.DB, name string) (string, error) {
	if !mysqlStatusNamePattern.MatchString(name) {
		return "", errors.New("invalid mysql status name")
	}
	var variable, value string
	err := db.QueryRowContext(ctx, `show session status where variable_name = '`+name+`'`).Scan(&variable, &value)
	return value, err
}

var mysqlStatusNamePattern = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
