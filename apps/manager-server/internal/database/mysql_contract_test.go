package database

import "testing"

func TestParseSupportedMySQLVersion(t *testing.T) {
	for _, version := range []string{
		"8.0.12",
		"8.0.12-commercial",
		"8.0.36",
		"8.1.0",
		"8.4.7-cloud",
	} {
		if _, err := ParseSupportedMySQLVersion(version, "MySQL Community Server"); err != nil {
			t.Errorf("supported version %q was rejected: %v", version, err)
		}
	}
	for _, test := range []struct {
		version string
		comment string
	}{
		{version: "8.0.11", comment: "MySQL Community Server"},
		{version: "5.7.44", comment: "MySQL Community Server"},
		{version: "9.0.0", comment: "MySQL Community Server"},
		{version: "8.0.36-MariaDB", comment: "MariaDB Server"},
		{version: "10.11.8", comment: "MariaDB Server"},
		{version: "invalid", comment: "MySQL"},
	} {
		if _, err := ParseSupportedMySQLVersion(test.version, test.comment); err == nil {
			t.Errorf("unsupported server %q/%q was accepted", test.version, test.comment)
		}
	}
}

func TestMySQLVersionLongTextDefaultCapability(t *testing.T) {
	legacy, err := ParseSupportedMySQLVersion("8.0.12", "MySQL Community Server")
	if err != nil {
		t.Fatal(err)
	}
	if legacy.SupportsLongTextDefaults() {
		t.Fatal("MySQL 8.0.12 unexpectedly supports LONGTEXT expression defaults")
	}
	modern, err := ParseSupportedMySQLVersion("8.0.13", "MySQL Community Server")
	if err != nil {
		t.Fatal(err)
	}
	if !modern.SupportsLongTextDefaults() {
		t.Fatal("MySQL 8.0.13 should support LONGTEXT expression defaults")
	}
}

func TestMySQLVersionSelectsAvailableNoPadCollation(t *testing.T) {
	for _, test := range []struct {
		version string
		want    string
	}{
		{version: "8.0.12", want: MySQLLegacyNoPadCollation},
		{version: "8.0.16", want: MySQLLegacyNoPadCollation},
		{version: "8.0.17", want: MySQLBinaryCollation},
		{version: "8.4.0", want: MySQLBinaryCollation},
	} {
		got, err := MySQLStorageCollation(test.version, "MySQL Community Server")
		if err != nil {
			t.Fatalf("select storage collation for %s: %v", test.version, err)
		}
		if got != test.want {
			t.Errorf("storage collation for %s = %q, want %q", test.version, got, test.want)
		}
	}
}
