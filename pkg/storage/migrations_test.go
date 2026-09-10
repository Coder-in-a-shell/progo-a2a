package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"testing/fstest"
)

func TestLoadMigrations_Success(t *testing.T) {
	sql1 := "CREATE TABLE test1 (id INT);"
	sql2 := "CREATE TABLE test2 (id INT);"

	mockFS := fstest.MapFS{
		"migrations/002_create_test2.sql": &fstest.MapFile{Data: []byte(sql2)},
		"migrations/001_create_test1.sql": &fstest.MapFile{Data: []byte(sql1)},
	}

	migrations, err := LoadMigrations(mockFS)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(migrations) != 2 {
		t.Fatalf("expected 2 migrations, got %d", len(migrations))
	}

	// Verify order
	if migrations[0].Version != 1 || migrations[0].Name != "001_create_test1.sql" {
		t.Fatalf("unexpected migration[0]: %+v", migrations[0])
	}
	if migrations[1].Version != 2 || migrations[1].Name != "002_create_test2.sql" {
		t.Fatalf("unexpected migration[1]: %+v", migrations[1])
	}

	// Verify checksums
	expectedSum1 := sha256.Sum256([]byte(sql1))
	expectedHex1 := hex.EncodeToString(expectedSum1[:])
	if migrations[0].Checksum != expectedHex1 {
		t.Fatalf("expected checksum %s, got %s", expectedHex1, migrations[0].Checksum)
	}

	expectedSum2 := sha256.Sum256([]byte(sql2))
	expectedHex2 := hex.EncodeToString(expectedSum2[:])
	if migrations[1].Checksum != expectedHex2 {
		t.Fatalf("expected checksum %s, got %s", expectedHex2, migrations[1].Checksum)
	}
}

func TestLoadMigrations_Duplicates(t *testing.T) {
	mockFS := fstest.MapFS{
		"migrations/001_alpha.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
		"migrations/001_beta.sql":  &fstest.MapFile{Data: []byte("SELECT 2;")},
	}

	_, err := LoadMigrations(mockFS)
	if err == nil {
		t.Fatal("expected duplicate migration error, got nil")
	}
	if !errors.Is(err, ErrInvalidMigration) {
		t.Fatalf("expected ErrInvalidMigration, got %v", err)
	}
}

func TestLoadMigrations_NonMonotonicGaps(t *testing.T) {
	mockFS := fstest.MapFS{
		"migrations/001_alpha.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
		"migrations/003_gamma.sql": &fstest.MapFile{Data: []byte("SELECT 3;")},
	}

	_, err := LoadMigrations(mockFS)
	if err == nil {
		t.Fatal("expected non-monotonic version error, got nil")
	}
	if !errors.Is(err, ErrInvalidMigration) {
		t.Fatalf("expected ErrInvalidMigration, got %v", err)
	}
}

func TestLoadMigrations_InvalidFilenames(t *testing.T) {
	tests := []struct {
		name     string
		filename string
	}{
		{"no underscore", "001table.sql"},
		{"non-numeric version", "abc_table.sql"},
		{"zero version", "000_table.sql"},
		{"negative version", "-1_table.sql"},
		{"no name part", "001_.sql"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mockFS := fstest.MapFS{
				"migrations/" + tc.filename: &fstest.MapFile{Data: []byte("SELECT 1;")},
			}
			_, err := LoadMigrations(mockFS)
			if err == nil {
				t.Fatalf("expected error for filename %s, got nil", tc.filename)
			}
			if !errors.Is(err, ErrInvalidMigration) {
				t.Fatalf("expected ErrInvalidMigration for %s, got %v", tc.filename, err)
			}
		})
	}
}

func TestLoadMigrations_Embedded(t *testing.T) {
	migrations, err := LoadMigrations(embeddedMigrationsFS)
	if err != nil {
		t.Fatalf("failed to load embedded migrations: %v", err)
	}

	if len(migrations) < 2 {
		t.Fatalf("expected at least 2 embedded migrations, got %d", len(migrations))
	}

	if migrations[0].Version != 1 || migrations[0].Name != "001_create_task_results.sql" {
		t.Fatalf("unexpected first migration: %+v", migrations[0])
	}
	if migrations[1].Version != 2 || migrations[1].Name != "002_create_durable_jobs.sql" {
		t.Fatalf("unexpected second migration: %+v", migrations[1])
	}

	for _, m := range migrations {
		if len(m.Checksum) != 64 {
			t.Fatalf("expected 64 hex characters for SHA-256 in %s, got %d", m.Name, len(m.Checksum))
		}
		if len(m.SQL) == 0 {
			t.Fatalf("expected non-empty SQL for %s", m.Name)
		}
	}
}
