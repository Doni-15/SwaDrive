// Package database opens and migrates SwaDrive's SQLite application database.
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"

	_ "modernc.org/sqlite"
)

const (
	driverName        = "sqlite"
	busyTimeoutMillis = "5000"
	// WAL allows concurrent readers, but SQLite still serializes writers. Four
	// connections are enough to let reads progress without manufacturing a
	// large pool of writers that can only contend on the same database file.
	maxOpenConnections = 4
)

var ErrDatabasePathRequired = errors.New("database path is required")

// Open opens a SQLite database at path. The caller owns the returned database
// handle and must close it with database/sql's Close method.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, ErrDatabasePathRequired
	}
	if strings.ContainsRune(path, '\x00') {
		return nil, fmt.Errorf("%w: path contains a null byte", ErrDatabasePathRequired)
	}

	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}

	db.SetMaxOpenConns(maxOpenConnections)
	db.SetMaxIdleConns(maxOpenConnections)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize sqlite database: %w", err)
	}

	return db, nil
}

func dataSourceName(path string) string {
	query := url.Values{}
	query.Set("_busy_timeout", busyTimeoutMillis)
	query.Set("_defensive", "1")
	query.Set("_foreign_keys", "on")
	query.Set("_journal_mode", "wal")
	query.Set("_synchronous", "full")
	// Every explicit transaction in the backend mutates state. Acquiring the
	// single SQLite writer slot at BeginTx avoids deferred read-to-write upgrade
	// failures while the busy timeout is still able to wait for the prior writer.
	query.Set("_txlock", "immediate")

	return (&url.URL{
		Scheme:   "file",
		Path:     path,
		RawQuery: query.Encode(),
	}).String()
}
