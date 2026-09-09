package mysql

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
)

func TestConfigDefaultsToVerifiedTLSAndRedactsPassword(t *testing.T) {
	config := Config{Host: "db.example.test", Database: "cpamp", Username: "manager", Password: "top-secret"}
	dsn, err := config.DSN()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dsn, "tls=cpamp_") {
		t.Fatalf("DSN does not select verified custom TLS: %s", dsn)
	}
	if !strings.Contains(dsn, "collation=utf8mb4_bin") ||
		!strings.Contains(dsn, "collation_connection=utf8mb4_0900_bin") ||
		!strings.Contains(dsn, "charset=utf8mb4") {
		t.Fatalf("DSN does not bootstrap and enforce SQLite-compatible NO PAD text semantics: %s", dsn)
	}
	diagnostic, err := config.driverConfigWithSessionCollation("")
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := diagnostic.Params["collation_connection"]; exists {
		t.Fatal("diagnostic connection prematurely requires the MySQL 8 session collation")
	}
	redacted, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(redacted), config.Password) {
		t.Fatal("Config JSON exposed mysql password")
	}
	view := config.Redacted()
	if !view.HasPassword || view.TLSMode != TLSVerifyIdentity || view.Port != 3306 {
		t.Fatalf("redacted defaults = %#v", view)
	}
	if strings.Contains(view.Host, "db.example") || strings.Contains(config.MaskedEndpoint(), "db.example") {
		t.Fatal("redacted mysql result exposed the complete host")
	}
}

func TestConfigDisabledTLSIsExplicit(t *testing.T) {
	config := Config{Host: "127.0.0.1", Database: "cpamp", Username: "manager", TLSMode: TLSDisabled}
	dsn, err := config.DSN()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dsn, "tls=false") {
		t.Fatalf("disabled TLS DSN = %s", dsn)
	}
}

func TestConfigDefaultsDoNotExpireLongRunningQueries(t *testing.T) {
	config := Config{Host: "127.0.0.1", Database: "cpamp", Username: "manager", TLSMode: TLSDisabled}
	driverConfig, err := config.driverConfigWithSessionCollation("")
	if err != nil {
		t.Fatal(err)
	}
	if driverConfig.ReadTimeout != 0 || driverConfig.WriteTimeout != 0 {
		t.Fatalf("default mysql I/O timeouts = read:%v write:%v, want both disabled", driverConfig.ReadTimeout, driverConfig.WriteTimeout)
	}

	config.ReadTimeout = 11 * time.Second
	config.WriteTimeout = 13 * time.Second
	driverConfig, err = config.driverConfigWithSessionCollation("")
	if err != nil {
		t.Fatal(err)
	}
	if driverConfig.ReadTimeout != config.ReadTimeout || driverConfig.WriteTimeout != config.WriteTimeout {
		t.Fatalf("explicit mysql I/O timeouts were not preserved: read:%v write:%v", driverConfig.ReadTimeout, driverConfig.WriteTimeout)
	}
}

func TestEffectiveConnMaxIdleTimeStaysBelowServerWaitTimeout(t *testing.T) {
	if got := effectiveConnMaxIdleTime(5*time.Minute, 120); got != time.Minute {
		t.Fatalf("short-server idle lifetime = %v, want 1m", got)
	}
	if got := effectiveConnMaxIdleTime(30*time.Second, 120); got != 30*time.Second {
		t.Fatalf("explicit shorter idle lifetime = %v, want 30s", got)
	}
	if got := effectiveConnMaxIdleTime(5*time.Minute, 0); got != 5*time.Minute {
		t.Fatalf("unknown-server idle lifetime = %v, want configured value", got)
	}
}

func TestConfigRejectsInvalidValues(t *testing.T) {
	tests := []Config{
		{Database: "cpamp", Username: "user"},
		{Host: "db", Database: "bad/name", Username: "user"},
		{Host: "db", Database: "cpamp", Username: "user", Port: 70000},
		{Host: "db", Database: "cpamp", Username: "user", TLSMode: "skip-verify"},
		{Host: "db", Database: "cpamp", Username: "user", ReadTimeout: -time.Second},
	}
	for _, config := range tests {
		if err := config.Validate(); err == nil {
			t.Fatalf("Validate(%#v) succeeded", config)
		}
	}
}

func TestValidateServerVersion(t *testing.T) {
	for _, version := range []string{"8.0.12", "8.0.99-commercial", "8.1.0", "8.4.0"} {
		if err := validateServerVersion(version, "MySQL Community Server"); err != nil {
			t.Errorf("version %s: %v", version, err)
		}
	}
	for _, test := range []struct{ version, comment string }{
		{"8.0.11", "MySQL Community Server"}, {"9.0.0", "MySQL Community Server"}, {"10.11.8", "MariaDB Server"},
	} {
		if err := validateServerVersion(test.version, test.comment); err == nil {
			t.Errorf("unsupported version %s succeeded", test.version)
		}
	}
}

func TestValidateServerSettingsRequiresLosslessSessionContract(t *testing.T) {
	valid := TestResult{
		CharacterSet: database.MySQLCharacterSet, Collation: database.MySQLBinaryCollation,
		ConnectionCharacterSet: database.MySQLCharacterSet,
		ConnectionCollation:    database.MySQLBinaryCollation, TimeZone: "+00:00",
		SQLMode: "STRICT_TRANS_TABLES,NO_ENGINE_SUBSTITUTION", InnoDBStrictMode: true,
		MaxAllowedPacket: minimumMaxAllowedPacket,
	}
	if err := validateServerSettings(valid, database.MySQLBinaryCollation); err != nil {
		t.Fatal(err)
	}
	legacy := valid
	legacy.Collation = database.MySQLLegacyNoPadCollation
	legacy.ConnectionCollation = database.MySQLLegacyNoPadCollation
	if err := validateServerSettings(legacy, database.MySQLLegacyNoPadCollation); err != nil {
		t.Fatalf("MySQL 8.0.12 NO PAD session contract was rejected: %v", err)
	}
	tests := []struct {
		name string
		edit func(*TestResult)
	}{
		{name: "connection collation", edit: func(v *TestResult) { v.ConnectionCollation = "utf8mb4_bin" }},
		{name: "connection character set", edit: func(v *TestResult) { v.ConnectionCharacterSet = "latin1" }},
		{name: "time zone", edit: func(v *TestResult) { v.TimeZone = "SYSTEM" }},
		{name: "non-strict sql", edit: func(v *TestResult) { v.SQLMode = "NO_ENGINE_SUBSTITUTION" }},
		{name: "non-strict innodb", edit: func(v *TestResult) { v.InnoDBStrictMode = false }},
		{name: "small packet", edit: func(v *TestResult) { v.MaxAllowedPacket-- }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.edit(&candidate)
			if err := validateServerSettings(candidate, database.MySQLBinaryCollation); err == nil {
				t.Fatal("invalid server settings were accepted")
			}
		})
	}
	advisoryDefault := valid
	advisoryDefault.CharacterSet = "latin1"
	advisoryDefault.Collation = "latin1_swedish_ci"
	if err := validateServerSettings(advisoryDefault, database.MySQLBinaryCollation); err != nil {
		t.Fatalf("database defaults should not block explicit CPAMP table initialization: %v", err)
	}
}

func TestRequireMigrationCapabilitiesRejectsEveryMissingProbe(t *testing.T) {
	valid := TestResult{Capabilities: map[string]Capability{
		"read": {Available: true}, "write": {Available: true}, "ddl": {Available: true},
	}}
	if err := valid.RequireMigrationCapabilities(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"read", "write", "ddl"} {
		candidate := valid
		candidate.Capabilities = map[string]Capability{
			"read": {Available: true}, "write": {Available: true}, "ddl": {Available: true},
		}
		candidate.Capabilities[name] = Capability{Error: "denied"}
		if err := candidate.RequireMigrationCapabilities(); err == nil || !strings.Contains(err.Error(), name+": denied") {
			t.Fatalf("missing %s error = %v", name, err)
		}
	}
}

func TestCapabilityProbeErrorExplainsBinaryLoggingTriggerRestriction(t *testing.T) {
	message := capabilityProbeError("CREATE TRIGGER", &drivermysql.MySQLError{
		Number:  1419,
		Message: "You do not have the SUPER privilege and binary logging is enabled",
	})
	for _, required := range []string{
		"MySQL Error 1419",
		"log_bin_trust_function_creators=ON",
		"do not grant SUPER",
	} {
		if !strings.Contains(message, required) {
			t.Fatalf("capability error %q does not contain %q", message, required)
		}
	}

	ordinary := capabilityProbeError("REFERENCES", &drivermysql.MySQLError{
		Number:  1142,
		Message: "REFERENCES command denied",
	})
	if !strings.Contains(ordinary, "REFERENCES command denied") {
		t.Fatalf("ordinary capability error lost the driver message: %q", ordinary)
	}
}

func TestSamplerRatesRequireTwoSamples(t *testing.T) {
	sampler := NewSampler()
	// A stable pointer is sufficient; rates never dereference it.
	db := new(sql.DB)
	firstQPS, firstTPS := sampler.rates(db, time.Unix(1, 0), map[string]float64{"Questions": 10, "Com_commit": 2, "Com_rollback": 1}, nil)
	if firstQPS.Available || firstTPS.Available {
		t.Fatal("first sample unexpectedly returned rates")
	}
	qps, tps := sampler.rates(db, time.Unix(3, 0), map[string]float64{"Questions": 20, "Com_commit": 6, "Com_rollback": 1}, nil)
	if !qps.Available || qps.Value != 5 || !tps.Available || tps.Value != 2 {
		t.Fatalf("rates qps=%#v tps=%#v", qps, tps)
	}
}
