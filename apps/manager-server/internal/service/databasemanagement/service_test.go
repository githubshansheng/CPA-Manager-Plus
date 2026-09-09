package databasemanagement

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestMutationControlRequiresIdempotencyKey(t *testing.T) {
	err := (MutationControl{ExpectedGeneration: 3}).Validate()
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Validate() error = %v, want ErrInvalidRequest", err)
	}
}

func TestMutationControlRequiresExpectedGeneration(t *testing.T) {
	err := (MutationControl{IdempotencyKey: "operation-1"}).Validate()
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Validate() error = %v, want ErrInvalidRequest", err)
	}
}

func TestMySQLConnectionRequiresExplicitInsecureTLSConfirmation(t *testing.T) {
	input := MySQLConnectionInput{
		Address:  "localhost:3306",
		Database: "cpamp",
		Username: "cpamp",
		TLSMode:  "disabled",
	}
	if err := input.Validate(); !errors.Is(err, ErrUnsafeOperation) {
		t.Fatalf("Validate() error = %v, want ErrUnsafeOperation", err)
	}
	input.ConfirmInsecureTLS = true
	if err := input.Validate(); err != nil {
		t.Fatalf("Validate() with confirmation: %v", err)
	}
}

func TestMySQLConnectionRejectsURLAddress(t *testing.T) {
	input := MySQLConnectionInput{
		Address:  "mysql://localhost:3306",
		Database: "cpamp",
		Username: "cpamp",
		TLSMode:  "verify_identity",
	}
	if err := input.Validate(); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Validate() error = %v, want ErrInvalidRequest", err)
	}
}

func TestPersistentMySQLCACertificateIsContentAddressed(t *testing.T) {
	runtime := &Runtime{dataDir: t.TempDir()}
	firstPath, _, err := runtime.writeCACertificate("first CA certificate", true)
	if err != nil {
		t.Fatal(err)
	}
	secondPath, _, err := runtime.writeCACertificate("second CA certificate", true)
	if err != nil {
		t.Fatal(err)
	}
	if firstPath == secondPath || filepath.Base(firstPath) == "mysql-ca.pem" {
		t.Fatalf("content-addressed CA paths = %q / %q", firstPath, secondPath)
	}
	first, err := os.ReadFile(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(secondPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != "first CA certificate" || string(second) != "second CA certificate" {
		t.Fatalf("CA files were overwritten: first=%q second=%q", first, second)
	}
	reusedPath, _, err := runtime.writeCACertificate("first CA certificate", true)
	if err != nil || reusedPath != firstPath {
		t.Fatalf("same CA path = %q, want %q (err=%v)", reusedPath, firstPath, err)
	}
}
