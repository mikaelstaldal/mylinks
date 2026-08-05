package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

var ErrDuplicate = errors.New("duplicate")
var ErrNotFound = errors.New("not found")

// connectionOptions are appended to the database file name to form the DSN:
//   - busy_timeout makes a connection wait for a lock held by another
//     connection instead of failing immediately with SQLITE_BUSY,
//   - journal_mode=WAL lets readers and the writer work concurrently, so an
//     ongoing read no longer blocks a write,
//   - _txlock=immediate takes the write lock when the transaction begins,
//     where the busy timeout applies, rather than when it first writes.
//     This also means every transaction must be assumed to write: a
//     read-only transaction added later would needlessly block writers.
//
// The default synchronous=FULL is kept, WAL is fast enough without
// trading away durability.
const connectionOptions = "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_txlock=immediate"

// maxOpenConns bounds the connection pool, so that a burst of requests cannot
// open an unbounded number of SQLite connections. WAL allows several
// concurrent readers alongside a single writer; writers beyond the first wait
// for the busy timeout.
const maxOpenConns = 8

// maxIdleConns matches maxOpenConns: connections to a local file are cheap to
// keep but not to churn, since reopening one re-applies the pragmas above and
// re-maps the WAL shared memory index.
const maxIdleConns = maxOpenConns

// Link represents a saved web link.
type Link struct {
	ID          int64
	URL         string
	Title       string
	Description string
	AddedAt     time.Time
}

// DB is a wrapper around sql.DB.
type DB struct {
	*sql.DB
}

// InitDB initializes the database.
func InitDB(databaseFile string) (*DB, error) {
	// An absolute path cannot be mistaken for a "file:" URI by the driver.
	databaseFile, err := filepath.Abs(databaseFile)
	if err != nil {
		return nil, err
	}
	// The driver splits the DSN at the first '?', so a file name containing
	// one would silently open a different database.
	if strings.ContainsRune(databaseFile, '?') {
		return nil, fmt.Errorf("database file name must not contain '?': %s", databaseFile)
	}

	db, err := sql.Open("sqlite", databaseFile+connectionOptions)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxIdleConns)
	// Leaving the database open on a failure here would leave its WAL file
	// behind as well.
	defer func() {
		if err != nil {
			_ = db.Close()
		}
	}()

	if err = db.Ping(); err != nil {
		return nil, err
	}

	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer func(tx *sql.Tx) {
		_ = tx.Rollback()
	}(tx)

	_, err = tx.Exec(`
		CREATE TABLE IF NOT EXISTS links (
			id INTEGER PRIMARY KEY,
			url TEXT NOT NULL UNIQUE,
			title TEXT NOT NULL,
			description TEXT NOT NULL,
			added_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return nil, err
	}

	_, err = tx.Exec(`
		CREATE VIRTUAL TABLE IF NOT EXISTS links_fts USING fts5(title, description, body, content='', contentless_delete=1);        
		-- Trigger to keep the FTS index up to date.
		CREATE TRIGGER IF NOT EXISTS links_ad AFTER DELETE ON links BEGIN
		  DELETE FROM links_fts WHERE ROWID=old.id;
		END;
	`)
	if err != nil {
		return nil, err
	}

	err = tx.Commit()
	if err != nil {
		return nil, err
	}

	err = ensureWritable(db)
	if err != nil {
		return nil, err
	}

	return &DB{db}, nil
}

func ensureWritable(db *sql.DB) error {
	conn, err := db.Conn(context.Background())
	if err != nil {
		return err
	}
	defer func(conn *sql.Conn) {
		_ = conn.Close()
	}(conn)

	return conn.Raw(func(c any) error {
		if d, ok := c.(interface{ IsReadOnly(string) (bool, error) }); ok {
			// Use "main" for the primary database schema
			isReadOnly, err := d.IsReadOnly("main")
			if err != nil {
				return err
			}
			if isReadOnly {
				return fmt.Errorf("database is read-only")
			}
			return nil
		}

		return fmt.Errorf("cannot check if database is read-only")
	})
}

// GetAllLinks returns all links from the database.
func (db *DB) GetAllLinks(ctx context.Context) ([]Link, error) {
	rows, err := db.QueryContext(ctx, "SELECT id, url, title, description, added_at FROM links ORDER BY added_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var links []Link
	for rows.Next() {
		var link Link
		if err := rows.Scan(&link.ID, &link.URL, &link.Title, &link.Description, &link.AddedAt); err != nil {
			return nil, err
		}
		links = append(links, link)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return links, nil
}

// Search returns links from the database matching a search string.
func (db *DB) Search(ctx context.Context, s string) ([]Link, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT l.id, l.url, l.title, l.description, l.added_at
		FROM links_fts f INNER JOIN links l ON l.id=f.rowid
		WHERE links_fts MATCH ? ORDER BY rank
		`, s)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var links []Link
	for rows.Next() {
		var link Link
		if err := rows.Scan(&link.ID, &link.URL, &link.Title, &link.Description, &link.AddedAt); err != nil {
			return nil, err
		}
		links = append(links, link)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return links, nil
}

// AddLink adds a new link to the database.
func (db *DB) AddLink(ctx context.Context, url, title, description string, body []byte) (int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func(tx *sql.Tx) {
		_ = tx.Rollback()
	}(tx)

	result, err := tx.ExecContext(ctx, "INSERT INTO links (url, title, description) VALUES (?, ?, ?)", url, title, description)
	if err != nil {
		var sqliteErr *sqlite.Error
		if errors.As(err, &sqliteErr) && sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE {
			return 0, ErrDuplicate
		}
		return 0, err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}

	_, err = tx.ExecContext(ctx, "INSERT INTO links_fts(rowid, title, description, body) VALUES (?, ?, ?, ?)", id, title, description, body)
	if err != nil {
		return 0, err
	}

	err = tx.Commit()
	if err != nil {
		return 0, err
	}

	return id, nil
}

// GetLink returns a single link from the database,
// returns ErrNotFound if no row with the given id is found.
func (db *DB) GetLink(ctx context.Context, id int64) (Link, error) {
	var link Link
	err := db.QueryRowContext(ctx, "SELECT id, url, title, description, added_at FROM links WHERE id = ?", id).
		Scan(&link.ID, &link.URL, &link.Title, &link.Description, &link.AddedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Link{}, ErrNotFound
	case err != nil:
		return Link{}, err
	default:
		return link, nil
	}
}

// DeleteLink deletes a link from the database.
func (db *DB) DeleteLink(ctx context.Context, id int64) error {
	result, err := db.ExecContext(ctx, "DELETE FROM links WHERE id = ?", id)
	if err != nil {
		return err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateLink updates a link in the database.
func (db *DB) UpdateLink(ctx context.Context, id int64, title string, description string) error {
	result, err := db.ExecContext(ctx, "UPDATE links SET title = ?, description = ? WHERE id = ?", title, description, id)
	if err != nil {
		return err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
