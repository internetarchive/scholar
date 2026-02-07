package blobproc

import (
	"fmt"
	"sync"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

const urlmapSchema = `
create table if not exists map (
	url  text not null,
	sha1 text not null,
	timestamp datetime default CURRENT_TIMESTAMP
);
create index if not exists index_url_sha1 on map(url, sha1);
`

// URLMap wraps an sqlite3 database for URL and SHA1 lookups.
type URLMap struct {
	Path string
	mu   sync.Mutex
	db   *sqlx.DB
}

// EnsureDB creates a new database with schema, if it is not already set up.
func (u *URLMap) EnsureDB() error {
	if u.db != nil {
		return nil
	}
	u.mu.Lock()
	defer u.mu.Unlock()

	// Double-check after acquiring the lock
	if u.db != nil {
		return nil
	}

	db, err := sqlx.Connect("sqlite", u.Path)
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}

	_, err = db.Exec(urlmapSchema)
	if err != nil {
		db.Close() // Close the connection if schema setup fails
		return fmt.Errorf("failed to create schema: %w", err)
	}

	u.db = db
	return nil
}

// Close closes the database connection.
func (u *URLMap) Close() error {
	u.mu.Lock()
	defer u.mu.Unlock()

	if u.db != nil {
		err := u.db.Close()
		u.db = nil
		return err
	}
	return nil
}

// Insert inserts a new pair into the database. We lock at the application
// level to avoid 'database is locked (5) (SQLITE_BUSY)'. This will return an
// error if the database has not been initialized before.
func (u *URLMap) Insert(url, sha1 string) error {
	if u.db == nil {
		return fmt.Errorf("URLMap database not initialized")
	}

	u.mu.Lock()
	defer u.mu.Unlock()

	_, err := u.db.Exec(`insert into map (url, sha1) values (?, ?)`, url, sha1)
	if err != nil {
		return fmt.Errorf("failed to insert url/sha1 pair: %w", err)
	}
	return nil
}
