package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	ErrInvalidMigration  = errors.New("invalid embedded migration")
	ErrMigrationMismatch = errors.New("applied migration does not match embedded migration")
)

//go:embed migrations/*.sql
var embeddedMigrations embed.FS

type migration struct {
	version  int64
	name     string
	checksum string
	sql      string
}

type appliedMigration struct {
	name     string
	checksum string
}

const createMigrationsTable = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    checksum TEXT NOT NULL,
    applied_at INTEGER NOT NULL
) STRICT;
`

// Migrate applies every pending embedded migration in version order. Schema
// changes and their tracking records commit together in one transaction.
func Migrate(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return errors.New("migrate database: nil database")
	}

	foreignKeysEnabled, err := foreignKeysEnabled(ctx, db)
	if err != nil {
		return err
	}
	if !foreignKeysEnabled {
		return errors.New("migrate database: sqlite foreign keys are disabled")
	}

	migrations, err := loadMigrations()
	if err != nil {
		return err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, createMigrationsTable); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	applied, err := readAppliedMigrations(ctx, tx)
	if err != nil {
		return err
	}
	if err := validateAppliedMigrations(applied, migrations); err != nil {
		return err
	}

	for _, migration := range migrations {
		if _, ok := applied[migration.version]; ok {
			continue
		}

		if _, err := tx.ExecContext(ctx, migration.sql); err != nil {
			return fmt.Errorf("apply migration %s: %w", migration.name, err)
		}
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`,
			migration.version,
			migration.name,
			migration.checksum,
			time.Now().UTC().Unix(),
		); err != nil {
			return fmt.Errorf("record migration %s: %w", migration.name, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}

func foreignKeysEnabled(ctx context.Context, db *sql.DB) (bool, error) {
	var enabled int
	if err := db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&enabled); err != nil {
		return false, fmt.Errorf("read sqlite foreign_keys setting: %w", err)
	}
	return enabled == 1, nil
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(embeddedMigrations, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}

	migrations := make([]migration, 0, len(entries))
	seenVersions := make(map[int64]string, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		version, err := migrationVersion(entry.Name())
		if err != nil {
			return nil, err
		}
		if existing, ok := seenVersions[version]; ok {
			return nil, fmt.Errorf("%w: %s and %s have version %d", ErrInvalidMigration, existing, entry.Name(), version)
		}

		contents, err := embeddedMigrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		checksum := sha256.Sum256(contents)
		migrations = append(migrations, migration{
			version:  version,
			name:     entry.Name(),
			checksum: hex.EncodeToString(checksum[:]),
			sql:      string(contents),
		})
		seenVersions[version] = entry.Name()
	}

	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].version < migrations[j].version
	})
	return migrations, nil
}

func migrationVersion(name string) (int64, error) {
	base := strings.TrimSuffix(name, ".sql")
	versionText, description, ok := strings.Cut(base, "_")
	if !ok || versionText == "" || description == "" {
		return 0, fmt.Errorf("%w: %s must match <version>_<name>.sql", ErrInvalidMigration, name)
	}

	version, err := strconv.ParseInt(versionText, 10, 64)
	if err != nil || version <= 0 {
		return 0, fmt.Errorf("%w: %s has an invalid version", ErrInvalidMigration, name)
	}
	return version, nil
}

func readAppliedMigrations(ctx context.Context, tx *sql.Tx) (map[int64]appliedMigration, error) {
	rows, err := tx.QueryContext(ctx, `SELECT version, name, checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("read applied migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int64]appliedMigration)
	for rows.Next() {
		var version int64
		var record appliedMigration
		if err := rows.Scan(&version, &record.name, &record.checksum); err != nil {
			return nil, fmt.Errorf("scan applied migration: %w", err)
		}
		applied[version] = record
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate applied migrations: %w", err)
	}
	return applied, nil
}

func validateAppliedMigrations(applied map[int64]appliedMigration, migrations []migration) error {
	embedded := make(map[int64]migration, len(migrations))
	for _, migration := range migrations {
		embedded[migration.version] = migration
	}

	for version, record := range applied {
		migration, ok := embedded[version]
		if !ok {
			return fmt.Errorf("%w: database contains unknown version %d", ErrMigrationMismatch, version)
		}
		if record.name != migration.name || record.checksum != migration.checksum {
			return fmt.Errorf("%w: version %d", ErrMigrationMismatch, version)
		}
	}

	for index, migration := range migrations {
		_, isApplied := applied[migration.version]
		if isApplied != (index < len(applied)) {
			return fmt.Errorf("%w: applied migrations do not form a prefix", ErrMigrationMismatch)
		}
	}
	return nil
}
