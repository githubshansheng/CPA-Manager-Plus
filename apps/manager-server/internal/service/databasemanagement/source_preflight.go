package databasemanagement

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/schema"
)

// sourcePreflightReport is intentionally kept internal. The migration is not
// created until every authoritative SQLite value has proved that it can be
// represented by the canonical MySQL schema and by one MySQL protocol packet.
// Scanning is row-bounded so an existing multi-gigabyte SQLite database does
// not turn the preflight into an equally large in-memory operation.
type sourcePreflightReport struct {
	Tables         int
	Rows           int64
	LargestRowByte int64
}

func preflightSQLiteSource(
	ctx context.Context,
	db *sql.DB,
	tables []schema.Table,
	maxAllowedPacket int64,
) (sourcePreflightReport, error) {
	if db == nil {
		return sourcePreflightReport{}, errors.New("sqlite source is unavailable")
	}
	if maxAllowedPacket <= 0 {
		return sourcePreflightReport{}, errors.New("mysql max_allowed_packet is unavailable")
	}
	report := sourcePreflightReport{}
	for _, table := range tables {
		if table.Class != schema.ClassAuthoritative || table.SQLiteOnly {
			continue
		}
		if err := preflightSQLiteTable(ctx, db, table, maxAllowedPacket, &report); err != nil {
			return report, err
		}
		report.Tables++
	}
	return report, nil
}

func preflightSQLiteTable(
	ctx context.Context,
	db *sql.DB,
	table schema.Table,
	maxAllowedPacket int64,
	report *sourcePreflightReport,
) error {
	selected := make([]string, 0, len(table.Columns)*2)
	for _, column := range table.Columns {
		selected = append(selected, sqliteQuote(column.Name))
	}
	for _, column := range table.Columns {
		selected = append(selected, "typeof("+sqliteQuote(column.Name)+")")
	}
	rows, err := db.QueryContext(ctx, "SELECT "+strings.Join(selected, ",")+
		" FROM "+sqliteQuote(table.Name))
	if err != nil {
		return fmt.Errorf("preflight read %s: %w", table.Name, err)
	}
	defer rows.Close()

	for ordinal := int64(1); rows.Next(); ordinal++ {
		values := make([]any, len(table.Columns))
		storageClasses := make([]string, len(table.Columns))
		destinations := make([]any, 0, len(table.Columns)*2)
		for index := range values {
			destinations = append(destinations, &values[index])
		}
		for index := range storageClasses {
			destinations = append(destinations, &storageClasses[index])
		}
		if err := rows.Scan(destinations...); err != nil {
			return fmt.Errorf("preflight scan %s row %d: %w", table.Name, ordinal, err)
		}
		for index, column := range table.Columns {
			if err := validateSQLiteMySQLValue(column, storageClasses[index], values[index]); err != nil {
				return fmt.Errorf("preflight %s row %d column %s: %w",
					table.Name, ordinal, column.Name, err)
			}
		}
		packetBytes, err := mysqlExecutePacketBytes(values)
		if err != nil {
			return fmt.Errorf("preflight %s row %d: %w", table.Name, ordinal, err)
		}
		if packetBytes > report.LargestRowByte {
			report.LargestRowByte = packetBytes
		}
		if packetBytes >= maxAllowedPacket {
			return fmt.Errorf(
				"preflight %s row %d requires an estimated %d-byte MySQL packet, max_allowed_packet is %d",
				table.Name, ordinal, packetBytes, maxAllowedPacket,
			)
		}
		report.Rows++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("preflight iterate %s: %w", table.Name, err)
	}
	return nil
}

func validateSQLiteMySQLValue(column schema.Column, storageClass string, value any) error {
	storageClass = strings.ToLower(strings.TrimSpace(storageClass))
	if storageClass == "null" || value == nil {
		if storageClass != "null" || value != nil {
			return errors.New("SQLite NULL storage and driver value disagree")
		}
		if !column.Nullable || column.PrimaryKeyPosition > 0 {
			return errors.New("NULL is not representable by the target column")
		}
		return nil
	}

	switch column.Kind {
	case schema.KindInteger:
		if storageClass != "integer" {
			return fmt.Errorf("incompatible SQLite storage class %q, want integer", storageClass)
		}
		integer, ok := value.(int64)
		if !ok {
			return fmt.Errorf("incompatible integer driver type %T", value)
		}
		if err := validateMySQLIntegerRange(column.MySQLType, integer); err != nil {
			return err
		}
	case schema.KindReal:
		switch storageClass {
		case "real":
			floating, ok := value.(float64)
			if !ok {
				return fmt.Errorf("incompatible real driver type %T", value)
			}
			if math.IsNaN(floating) || math.IsInf(floating, 0) {
				return errors.New("non-finite floating value")
			}
		case "integer":
			integer, ok := value.(int64)
			if !ok {
				return fmt.Errorf("incompatible real/integer driver type %T", value)
			}
			// A SQLite REAL-affinity column may physically retain an integer.
			// MySQL DOUBLE cannot preserve larger integers bit-for-bit.
			if integer < -maxExactFloatInteger || integer > maxExactFloatInteger {
				return fmt.Errorf("integer %d cannot be represented exactly by DOUBLE", integer)
			}
		default:
			return fmt.Errorf("incompatible SQLite storage class %q, want real or exact integer", storageClass)
		}
	case schema.KindText:
		if storageClass != "text" {
			return fmt.Errorf("incompatible SQLite storage class %q, want text", storageClass)
		}
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("incompatible text driver type %T", value)
		}
		if !utf8.ValidString(text) {
			return errors.New("invalid UTF-8")
		}
		if err := validateMySQLTextRange(column.MySQLType, text); err != nil {
			return err
		}
	case schema.KindBlob:
		if storageClass != "blob" {
			return fmt.Errorf("incompatible SQLite storage class %q, want blob", storageClass)
		}
		if _, ok := value.([]byte); !ok {
			return fmt.Errorf("incompatible blob driver type %T", value)
		}
	default:
		return fmt.Errorf("unsupported logical column kind %q", column.Kind)
	}
	return nil
}

const maxExactFloatInteger = int64(1 << 53)

func validateMySQLIntegerRange(mysqlType string, value int64) error {
	typeName := strings.ToUpper(strings.TrimSpace(mysqlType))
	switch {
	case strings.HasPrefix(typeName, "TINYINT"):
		if value < math.MinInt8 || value > math.MaxInt8 {
			return fmt.Errorf("integer %d is outside signed TINYINT", value)
		}
	case strings.HasPrefix(typeName, "INT"):
		if value < math.MinInt32 || value > math.MaxInt32 {
			return fmt.Errorf("integer %d is outside signed INT", value)
		}
	case strings.HasPrefix(typeName, "BIGINT"):
		// database/sql already returned an int64, which is the exact target range.
	default:
		return fmt.Errorf("unsupported integer target type %q", mysqlType)
	}
	return nil
}

var boundedMySQLTextPattern = regexp.MustCompile(`(?i)^(?:VAR)?CHAR\((\d+)\)`)

func validateMySQLTextRange(mysqlType, value string) error {
	upperType := strings.ToUpper(strings.TrimSpace(mysqlType))
	match := boundedMySQLTextPattern.FindStringSubmatch(upperType)
	if len(match) == 2 {
		limit, err := strconv.Atoi(match[1])
		if err != nil {
			return fmt.Errorf("invalid target text type %q", mysqlType)
		}
		characters := utf8.RuneCountInString(value)
		if characters > limit {
			return fmt.Errorf("text contains %d characters, target %s permits %d", characters, mysqlType, limit)
		}
	}
	if strings.Contains(upperType, "CHARACTER SET ASCII") {
		for _, character := range []byte(value) {
			if character >= utf8.RuneSelf {
				return errors.New("non-ASCII text cannot be represented by the target ASCII column")
			}
		}
	}
	// MySQL CHAR retrieval discards trailing U+0020 even under a binary
	// collation. Reject it rather than silently changing an authoritative key.
	if strings.HasPrefix(upperType, "CHAR(") && strings.HasSuffix(value, " ") {
		return errors.New("trailing spaces are not lossless in a MySQL CHAR column")
	}
	return nil
}

// mysqlExecutePacketBytes estimates COM_STMT_EXECUTE precisely enough to
// reject a row before go-sql-driver/mysql sends it. The statement preparation
// is a separate, much smaller packet. Length-encoded strings include their
// protocol prefix; numeric parameters use eight bytes.
func mysqlExecutePacketBytes(values []any) (int64, error) {
	size := int64(1 + 4 + 1 + 4 + (len(values)+7)/8 + 1 + 2*len(values))
	for _, value := range values {
		switch typed := value.(type) {
		case nil:
		case int64, float64, bool:
			size += 8
		case string:
			length := int64(len(typed))
			size += length + mysqlLengthEncodedPrefixBytes(length)
		case []byte:
			length := int64(len(typed))
			size += length + mysqlLengthEncodedPrefixBytes(length)
		default:
			return 0, fmt.Errorf("cannot encode MySQL parameter type %T", value)
		}
	}
	return size, nil
}

func mysqlLengthEncodedPrefixBytes(length int64) int64 {
	switch {
	case length < 251:
		return 1
	case length < 1<<16:
		return 3
	case length < 1<<24:
		return 4
	default:
		return 9
	}
}

func mysqlSessionMaxAllowedPacket(ctx context.Context, db *sql.DB) (int64, error) {
	if db == nil {
		return 0, errors.New("mysql is unavailable")
	}
	var limit int64
	if err := db.QueryRowContext(ctx, `SELECT @@session.max_allowed_packet`).Scan(&limit); err != nil {
		return 0, fmt.Errorf("read mysql max_allowed_packet: %w", err)
	}
	if limit <= 0 {
		return 0, fmt.Errorf("mysql returned invalid max_allowed_packet %d", limit)
	}
	return limit, nil
}
