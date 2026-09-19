# MyLinks Project Guidelines

This document provides essential information for developers working on the MyLinks project.

## Build/Configuration Instructions

### Prerequisites
- Go, at least the version in the `go` directive of `go.mod`
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

### Upgrading Go
The `go` directive in `go.mod` names an exact patch release rather than just a
minor version, so that a toolchain carrying a security fix is a hard
requirement rather than a suggestion. Two consequences:

- The `golang:` base image in the `Dockerfile` has to be raised to match. The
  official images set `GOTOOLCHAIN=local`, so a base image older than the `go`
  directive cannot build this at all. Bumping one file without the other
  breaks the image build, which is why CI builds the image on every push.
- Anyone building with `GOTOOLCHAIN=local` needs that exact patch release
  installed. Under the default `GOTOOLCHAIN=auto` the right toolchain is
  downloaded automatically.

CI needs no change on a bump: `actions/setup-go` reads the version from
`go.mod`.

### Files that have to move together
Beyond `go.mod` and the `Dockerfile` above, five couplings are held together
by nothing but attention:

- Adding a `COPY` to the `Dockerfile` means adding the same path to
  `.dockerignore`, which is an allow list. Forgetting fails the image build,
  so CI catches it, but the error names the missing file rather than the cause.
- The `govulncheck` version is pinned in both `.github/workflows/main.yml` and
  `.github/workflows/govulncheck.yml`. Bump both.
- The `chromedp/headless-shell` digest in the `Dockerfile` is pinned and so is
  frozen until someone raises it by hand. `govulncheck` scans the Go code, not
  the image, and will never report anything about it, so nothing here notices
  when that base image accumulates vulnerabilities.
- The number in `ui/static/style.N.css` is a cache buster, so changing the file
  usually means renaming it — and the name appears in **both**
  `ui/templates/index.html` and `ui/templates/bookmarklet-result.html`. Updating
  only one leaves that page silently unstyled: the 404 is for a stylesheet, so
  nothing fails, the page just renders bare.
- The brand row at the top of `index.html` — the badge, the "MyLinks" label and
  the 8px between them — implements `../mysuite/spec/app-logo.md` and
  `../mysuite/spec/app-name-label.md`, contracts shared with MyCal, MyMail and
  MyNotes. See below.
- `ui/static/favicon.svg`, `ui/static/favicon.ico` and the badge's inline `<svg>`
  in `ui/templates/index.html` are three copies of one drawing: the same three
  bars, and the same `#2563eb` behind them. Change one and change all three. The
  `.ico` is a 16x16 raster, so it needs re-rendering (or recolouring) rather than
  editing. Nothing warns you — a stale favicon just looks like the old app.

### The brand row answers to a spec in another repository
`.brand`, `.brand-logo`, `.brand-name` and `.app-body`'s padding in
`ui/static/style.N.css` are not free choices. The rendered values they produce
are fixed by `../mysuite/spec/app-logo.md` and `../mysuite/spec/app-name-label.md`:
a 28×28 badge at (16, 14) from the window holding a 17×17 glyph, 8px of gap, and
a `1.1rem` label in `system-ui, -apple-system, "Segoe UI", Roboto, sans-serif`.
The mechanism is ours — those contracts bind the observable result, not how a
project reaches it, which is why MyLinks keeps missing.css, htmx and hyperscript
while matching the three apps' geometry.

The favicon follows the same drawing. Its square is the badge's light-theme
`#2563eb` and does not invert in dark mode, only the badge does — which is what
MyCal and MyNotes do with theirs (`app-logo.md` §6.3). Favicon/badge parity is
recorded there, not mandated, so this is a house rule rather than a contract
value; it is worth keeping because the badge is the favicon's mark with the
square taken off.

**Nothing in this repository checks any of it**, and a spec in a sibling checkout
is not something CI can see. Two edits in particular look local and are not:
changing `.app-body`'s `padding` moves the badge off (16, 14), and replacing
`.brand-name`'s `font-size: 1.1rem` with the equivalent `17.6px` agrees at the
default root font size and disagrees at every other one. The comments in the
stylesheet name the section of the spec each value comes from; read the spec
before changing a number, and change the spec with the owner before changing it
here.

### Configuration Options
The application accepts the following command-line flags:
- `-port <number>`: port to listen on (default: 8080)
- `-addr <address>`: address to listen on (default: 127.0.0.1)
- `-data <directory>`: directory to store data in (default: data)
- `-basic-auth-file <file>`: enable HTTP basic auth with credentials from this
  file, in htpasswd format, bcrypt only
- `-basic-auth-realm <realm>`: realm for HTTP basic auth (default: mylinks)
- `-public-url <url>`: public-facing base URL, used for CSRF validation
  (defaults to `http://<addr>:<port>`). Behind a reverse proxy this has to be
  set to the externally visible URL or every state-changing request is
  rejected. See `OPERATIONS.md`.
- `-version`: print version information and exit

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