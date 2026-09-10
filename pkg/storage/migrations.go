package storage

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var embeddedMigrationsFS embed.FS

const (
	migrationLockID = int64(0x5461736b53746f72) // "TaskStor"
)

var (
	// ErrMigrationChecksumMismatch is returned when an already-recorded migration checksum differs from the current embedded SQL.
	ErrMigrationChecksumMismatch = errors.New("migration checksum mismatch")
	// ErrInvalidMigration is returned when migration files are named improperly, have non-monotonic versions, or contain duplicates.
	ErrInvalidMigration = errors.New("invalid migration definition")
	// ErrMigrationNameMismatch is returned when a recorded migration version has a different filename.
	ErrMigrationNameMismatch = errors.New("migration name mismatch")
	// ErrMigrationVersionTooNew is returned when the database contains a migration unknown to this binary.
	ErrMigrationVersionTooNew = errors.New("database migration version is newer than this binary")
)

// Migration represents a single versioned database migration.
type Migration struct {
	Version  int
	Name     string
	Checksum string // lowercase hex SHA-256
	SQL      string
}

// LoadMigrations parses and validates SQL migration files from the provided filesystem.
// It enforces unique, strictly monotonically increasing version numbers starting at 1.
func LoadMigrations(fsys fs.FS) ([]Migration, error) {
	var entries []fs.DirEntry
	var baseDir string

	if dirEntries, err := fs.ReadDir(fsys, "migrations"); err == nil {
		entries = dirEntries
		baseDir = "migrations"
	} else {
		rootEntries, rerr := fs.ReadDir(fsys, ".")
		if rerr != nil {
			return nil, fmt.Errorf("%w: failed to read migration directory: %v", ErrInvalidMigration, rerr)
		}
		entries = rootEntries
		baseDir = "."
	}

	var migrations []Migration
	seenVersions := make(map[int]string)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}

		parts := strings.SplitN(name, "_", 2)
		if len(parts) != 2 || len(parts[0]) == 0 {
			return nil, fmt.Errorf("%w: migration filename %q must follow <version>_<name>.sql pattern", ErrInvalidMigration, name)
		}

		logicalName := strings.TrimSuffix(parts[1], ".sql")
		if strings.TrimSpace(logicalName) == "" {
			return nil, fmt.Errorf("%w: migration filename %q has empty logical name before .sql", ErrInvalidMigration, name)
		}

		version, err := strconv.Atoi(parts[0])
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("%w: migration filename %q has invalid version %q: %v", ErrInvalidMigration, name, parts[0], err)
		}

		if prevName, exists := seenVersions[version]; exists {
			return nil, fmt.Errorf("%w: duplicate migration version %d in %q and %q", ErrInvalidMigration, version, prevName, name)
		}
		seenVersions[version] = name

		filePath := path.Join(baseDir, name)
		content, err := fs.ReadFile(fsys, filePath)
		if err != nil {
			return nil, fmt.Errorf("%w: failed to read migration file %q: %v", ErrInvalidMigration, filePath, err)
		}

		sum := sha256.Sum256(content)
		checksum := hex.EncodeToString(sum[:])

		migrations = append(migrations, Migration{
			Version:  version,
			Name:     name,
			Checksum: checksum,
			SQL:      string(content),
		})
	}

	if len(migrations) == 0 {
		return nil, fmt.Errorf("%w: no migration files found", ErrInvalidMigration)
	}

	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})

	for i := 0; i < len(migrations); i++ {
		expectedVersion := i + 1
		if migrations[i].Version != expectedVersion {
			return nil, fmt.Errorf("%w: non-monotonic migration version: expected %d, got %d for %q",
				ErrInvalidMigration, expectedVersion, migrations[i].Version, migrations[i].Name)
		}
	}

	return migrations, nil
}

// runMigrations executes the versioned migrations inside a single transaction holding an advisory lock.
// It verifies existing checksums and applies new migrations in order.
func runMigrations(ctx context.Context, pool *pgxpool.Pool, tokens []string) error {
	if pool == nil {
		return errors.New("cannot run migrations with nil pool")
	}

	migrations, err := LoadMigrations(embeddedMigrationsFS)
	if err != nil {
		return err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return redactError(err, tokens)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	// Hold transaction-scoped advisory lock for the duration of migration check and application
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", migrationLockID); err != nil {
		return fmt.Errorf("acquire advisory lock: %w", redactError(err, tokens))
	}

	// Ensure schema_migrations table exists
	const createSchemaMigrationsTableSQL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    checksum TEXT NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
`
	if _, err := tx.Exec(ctx, createSchemaMigrationsTableSQL); err != nil {
		return fmt.Errorf("create schema_migrations table: %w", redactError(err, tokens))
	}

	// Fetch already applied migrations
	rows, err := tx.Query(ctx, "SELECT version, name, checksum FROM schema_migrations ORDER BY version ASC")
	if err != nil {
		return fmt.Errorf("query schema_migrations: %w", redactError(err, tokens))
	}

	type recordedMigration struct {
		Name     string
		Checksum string
	}
	applied := make(map[int]recordedMigration)
	for rows.Next() {
		var v int
		var rec recordedMigration
		if err := rows.Scan(&v, &rec.Name, &rec.Checksum); err != nil {
			rows.Close()
			return fmt.Errorf("scan schema_migrations: %w", redactError(err, tokens))
		}
		applied[v] = rec
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate schema_migrations: %w", redactError(err, tokens))
	}

	known := make(map[int]Migration, len(migrations))
	for _, migration := range migrations {
		known[migration.Version] = migration
	}
	for version, recorded := range applied {
		migration, exists := known[version]
		if !exists {
			return fmt.Errorf("%w: recorded version %d (%s)", ErrMigrationVersionTooNew, version, recorded.Name)
		}
		if recorded.Name != migration.Name {
			return fmt.Errorf("%w: version %d recorded as %s, expected %s", ErrMigrationNameMismatch, version, recorded.Name, migration.Name)
		}
	}

	// Check checksums of already applied migrations and apply new ones
	for _, m := range migrations {
		if rec, exists := applied[m.Version]; exists {
			if rec.Checksum != m.Checksum {
				return fmt.Errorf("%w: version %d (%s) recorded checksum %s does not match current %s",
					ErrMigrationChecksumMismatch, m.Version, m.Name, rec.Checksum, m.Checksum)
			}
			continue
		}

		// Apply unapplied migration
		if _, err := tx.Exec(ctx, m.SQL); err != nil {
			return fmt.Errorf("execute migration %s: %w", m.Name, redactError(err, tokens))
		}

		// Record applied migration in schema_migrations
		const recordMigrationSQL = `
INSERT INTO schema_migrations (version, name, checksum, applied_at)
VALUES ($1, $2, $3, NOW());
`
		if _, err := tx.Exec(ctx, recordMigrationSQL, m.Version, m.Name, m.Checksum); err != nil {
			return fmt.Errorf("record migration %s: %w", m.Name, redactError(err, tokens))
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migrations: %w", redactError(err, tokens))
	}

	return nil
}
