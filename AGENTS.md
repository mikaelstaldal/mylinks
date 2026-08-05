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
    database.AddLink("https://example.com", "Example Website", "", nil)
    
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
- `cmd/mylinks/main.go`: Application entry point, server configuration, and route setup
- `cmd/mylinks/db/`: Database operations and models
  - `db.go`: Database initialization and CRUD operations
  - `db_test.go`: Tests for database operations
- `cmd/mylinks/web/`: HTTP request handlers
  - `handlers.go`: Handler implementations for routes
  - `handlers_test.go`: Tests for handlers
- `ui/templates/`: HTML templates
  - `index.html`: Main page
- `ui/static/`: Static assets (CSS, JavaScript, etc.)

### Code Style Guidelines
- Follow standard Go code style and conventions
- Use meaningful variable and function names
- Add comments for non-obvious code sections
- Keep functions focused on a single responsibility
- Use proper error handling with descriptive error messages

### Database Schema
The application uses a single SQLite table:
```sql
CREATE TABLE IF NOT EXISTS links (
    id INTEGER PRIMARY KEY,
    url TEXT NOT NULL,
    title TEXT NOT NULL,
    added_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
)
```

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