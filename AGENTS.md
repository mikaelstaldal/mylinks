# MyLinks Project Guidelines

This document provides essential information for developers working on the MyLinks project.

## Build/Configuration Instructions

### Prerequisites
- Go 1.26 or later
- SQLite support (provided by modernc.org/sqlite)

### Building the Project
1. Clone the repository
2. Navigate to the project root
3. Build the application:
   ```bash
   go build ./cmd/mylinks
   ```
4. Run the application:
   ```bash
   ./mylinks
   ```

### Configuration Options
The application accepts the following command-line flags:
- `-port <number>`: Specify the HTTP server port (default: 8080)

Example:
```bash
./mylinks -port 9000
```

## Testing Information

### Running Tests
To run all tests:
```bash
go test ./...
```

To run tests for a specific package with verbose output:
```bash
go test -v ./cmd/mylinks/db
go test -v ./cmd/mylinks/web
```

### Test Structure
- Tests use temporary database files that are removed after test completion
- HTTP handlers are tested using `net/http/httptest` package
- Assertions are made using `github.com/stretchr/testify/assert` and `github.com/stretchr/testify/require`
- Each test focuses on a specific functionality (e.g., adding, retrieving, or deleting links)

### Adding New Tests
1. Create a test file with the naming convention `*_test.go` in the relevant package directory
2. For database tests, follow the pattern in `cmd/mylinks/db/db_test.go`:
   - Use a database file in `t.TempDir()`, so the WAL and shared memory files
     are cleaned up along with it
   - Close the database with `t.Cleanup()`
   - Test the full lifecycle of operations

3. For handler tests, follow the pattern in `cmd/mylinks/web/handlers_test.go`:
   - Create a test database
   - Initialize the handler with the test database and templates
   - Use `httptest.NewRecorder()` to capture responses
   - Verify both status codes and response content using `testify` assertions

### Example Test
Here's a simple example of testing the ListLinks handler:

```go
func TestListLinks(t *testing.T) {
    // Use a temporary database file for testing
    dbFile := filepath.Join(t.TempDir(), "test_handlers.db")
    
    // Initialize the database and add test data
    database, _ := db.InitDB(dbFile)
    database.AddLink(t.Context(), "https://example.com", "Example Website", "", nil)
    
    // Create handler
    h := NewHandlers(".", database, "")
    
    // Create request and response recorder
    req := httptest.NewRequest("GET", "/", nil)
    rr := httptest.NewRecorder()
    
    // Call the handler and check response
    h.ListLinks(rr, req)
    
    assert.Equal(t, http.StatusOK, rr.Code)
    assert.Contains(t, rr.Body.String(), "Example Website")
}
```

## Additional Development Information

### Project Structure
- `cmd/mylinks/main.go`: Application entry point, server configuration, and shutdown
  - `main_test.go`: Tests which run the built server, for startup and shutdown
- `cmd/mylinks/db/`: Database operations and models
  - `db.go`: Database initialization and CRUD operations
  - `db_test.go`: Tests for database operations
- `cmd/mylinks/web/`: HTTP request handlers
  - `handlers.go`: Handler implementations for routes, and route setup
  - `middleware.go`: Common response headers
  - `handlers_test.go`: Tests for handlers
- `ui/templates/`: HTML templates
  - `index.html`: Main page, `links.html`: The list of links
  - `link-with-screenshot.html` / `link-without-screenshot.html`: A single link
  - `bookmarklet-result.html`: Result of saving from the bookmarklet
- `ui/static/`: Static assets (CSS, JavaScript, etc.)

### Code Style Guidelines
- Follow standard Go code style and conventions
- Use meaningful variable and function names
- Add comments for non-obvious code sections
- Keep functions focused on a single responsibility
- Use proper error handling with descriptive error messages
- Database operations take a `context.Context` as their first argument, pass
  `r.Context()` from handlers so that work is abandoned when the client goes away

### Database Schema
Links are stored in a single SQLite table:
```sql
CREATE TABLE IF NOT EXISTS links (
    id INTEGER PRIMARY KEY,
    url TEXT NOT NULL UNIQUE,
    title TEXT NOT NULL,
    description TEXT NOT NULL,
    added_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
)
```

Full text search uses a contentless FTS5 index, which also holds the page body.
It is populated by `AddLink`, rewritten by `UpdateLink`, and cleaned up by a
trigger on delete:
```sql
CREATE VIRTUAL TABLE IF NOT EXISTS links_fts USING fts5(title, description, body, content='', contentless_delete=1);

CREATE TABLE IF NOT EXISTS link_bodies (
    link_id INTEGER PRIMARY KEY,
    body BLOB NOT NULL
);

CREATE TRIGGER links_ad AFTER DELETE ON links BEGIN
  DELETE FROM links_fts WHERE ROWID=old.id;
  DELETE FROM link_bodies WHERE link_id=old.id;
END;
```

A contentless index cannot be read back, so an FTS row can only be written in
full, never updated in place. `link_bodies` keeps the page body so that
`UpdateLink` can delete and re-insert the FTS row with the new title and
description without dropping body matching.

A link has no row in `link_bodies` if its page yielded no body, or if it was
added before the table existed. `HasBody` reports on that, and `EditLink` then
fetches the page again to get a body to pass to `UpdateLink`, so that editing
such a link makes it searchable rather than leaving it without a body forever.
Refetching is best effort: the user asked to edit the link, not to fetch it, so
a failure is logged and the edit proceeds.

A note has no page to fetch, its text is its body. It is re-indexed from the
edited text on every edit, rather than carried forward, so that an edited note
stops matching the text it no longer holds.

The database is opened in WAL mode with a busy timeout, see `connectionOptions`
in `cmd/mylinks/db/db.go`. This means `mylinks.sqlite-wal` and
`mylinks.sqlite-shm` exist alongside the database file while it is open.

### Adding New Features
When adding new features:
1. Add necessary database operations in `cmd/mylinks/db/db.go`
2. Write tests for the new operations in `cmd/mylinks/db/db_test.go`
3. Implement handlers for new routes in `cmd/mylinks/web/handlers.go`
4. Write tests for the new handlers in `cmd/mylinks/web/handlers_test.go`
5. Create or modify templates as needed in the `ui/templates/` directory
6. Update route configuration in `cmd/mylinks/web/handlers.go` (Routes method)

### Debugging Tips
- Use `log.Printf()` for debugging information
- For database issues, you can examine the SQLite file directly using the SQLite CLI:
  ```bash
  sqlite3 mylinks.sqlite
  ```
- For template rendering issues, check the HTML source in the browser