package db

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"fmt"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

var ErrDuplicate = errors.New("duplicate")
var ErrNotFound = errors.New("not found")

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
	db, err := sql.Open("sqlite", databaseFile)
	if err != nil {
		return nil, err
	}

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
func (db *DB) GetAllLinks() ([]Link, error) {
	rows, err := db.Query("SELECT id, url, title, description, added_at FROM links ORDER BY added_at DESC")
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
func (db *DB) Search(s string) ([]Link, error) {
	rows, err := db.Query(`
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
func (db *DB) AddLink(url, title, description string, body []byte) (int64, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer func(tx *sql.Tx) {
		_ = tx.Rollback()
	}(tx)

	result, err := tx.Exec("INSERT INTO links (url, title, description) VALUES (?, ?, ?)", url, title, description)
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

	_, err = tx.Exec("INSERT INTO links_fts(rowid, title, description, body) VALUES (?, ?, ?, ?)", id, title, description, body)
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
func (db *DB) GetLink(id int64) (Link, error) {
	var link Link
	err := db.QueryRow("SELECT id, url, title, description, added_at FROM links WHERE id = ?", id).
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
func (db *DB) DeleteLink(id int64) error {
	result, err := db.Exec("DELETE FROM links WHERE id = ?", id)
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
func (db *DB) UpdateLink(id int64, title string, description string) error {
	result, err := db.Exec("UPDATE links SET title = ?, description = ? WHERE id = ?", title, description, id)
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
