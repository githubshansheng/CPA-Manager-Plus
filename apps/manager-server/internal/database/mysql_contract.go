package database

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// MySQLCharacterSet and the two collations below are the storage contract for
// CPAMP text. MySQL 8.0.17 introduced utf8mb4_0900_bin. Older supported
// servers use the closest NO PAD, case-sensitive and accent-sensitive 0900
// collation so values that differ only by trailing spaces remain distinct.
const (
	MySQLCharacterSet         = "utf8mb4"
	MySQLBinaryCollation      = "utf8mb4_0900_bin"
	MySQLLegacyNoPadCollation = "utf8mb4_0900_as_cs"
	MySQLMinimumVersion       = "8.0.12"
	mysqlBinaryCollationMinor = 0
	mysqlBinaryCollationPatch = 17
)

var mysqlVersionPattern = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)`)

// MySQLVersion is the parsed official MySQL server version used to select
// compatibility-safe SQL and DDL. MariaDB versions are rejected before this
// value is returned.
type MySQLVersion struct {
	major int
	minor int
	patch int
}

// ParseSupportedMySQLVersion validates the server family and the oldest
// release covered by CPAMP's compatibility SQL. MySQL innovation/LTS releases
// within major version 8 are accepted because they contain the 8.0.12 feature
// baseline; MySQL 9 remains opt-in until separately validated.
func ParseSupportedMySQLVersion(version, comment string) (MySQLVersion, error) {
	if strings.Contains(strings.ToLower(version+" "+comment), "mariadb") {
		return MySQLVersion{}, fmt.Errorf("MariaDB %q is not supported; MySQL %s+ is required",
			version, MySQLMinimumVersion)
	}
	parts := mysqlVersionPattern.FindStringSubmatch(strings.TrimSpace(version))
	if len(parts) != 4 {
		return MySQLVersion{}, fmt.Errorf("unrecognized mysql version %q", version)
	}
	major, _ := strconv.Atoi(parts[1])
	minor, _ := strconv.Atoi(parts[2])
	patch, _ := strconv.Atoi(parts[3])
	parsed := MySQLVersion{major: major, minor: minor, patch: patch}
	if major != 8 || !parsed.AtLeast(8, 0, 12) {
		return MySQLVersion{}, fmt.Errorf("MySQL %s is unsupported; MySQL %s+ is required",
			version, MySQLMinimumVersion)
	}
	return parsed, nil
}

// AtLeast reports whether the parsed release is at or above a feature's
// introduction version.
func (v MySQLVersion) AtLeast(major, minor, patch int) bool {
	if v.major != major {
		return v.major > major
	}
	if v.minor != minor {
		return v.minor > minor
	}
	return v.patch >= patch
}

// SupportsLongTextDefaults reports whether MySQL accepts expression defaults
// for BLOB/TEXT columns. This syntax was introduced in MySQL 8.0.13.
func (v MySQLVersion) SupportsLongTextDefaults() bool {
	return v.AtLeast(8, 0, 13)
}

// StorageCollation returns the strongest NO PAD collation available on the
// parsed server. MySQL 8.0.12-8.0.16 do not expose utf8mb4_0900_bin.
func (v MySQLVersion) StorageCollation() string {
	if v.AtLeast(8, mysqlBinaryCollationMinor, mysqlBinaryCollationPatch) {
		return MySQLBinaryCollation
	}
	return MySQLLegacyNoPadCollation
}

// MySQLStorageCollation validates a server version and returns its required
// table/session collation.
func MySQLStorageCollation(version, comment string) (string, error) {
	parsed, err := ParseSupportedMySQLVersion(version, comment)
	if err != nil {
		return "", err
	}
	return parsed.StorageCollation(), nil
}
