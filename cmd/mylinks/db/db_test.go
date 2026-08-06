package db

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

//goland:noinspection GoDirectComparisonOfErrors
func TestDB(t *testing.T) {
	// Use a temporary database file for testing, in a directory which is
	// removed afterwards along with the WAL and shared memory files.
	dbFile := filepath.Join(t.TempDir(), "test.database")

	// Initialize the database
	database, err := InitDB(dbFile)
	require.NoError(t, err, "Failed to initialize database")

	t.Cleanup(func() {
		if database != nil {
			_ = database.Close()
		}
	})

	// Verify the connection options which keep concurrent access working
	var journalMode string
	err = database.QueryRow("PRAGMA journal_mode").Scan(&journalMode)
	require.NoError(t, err, "Failed to query journal_mode")
	assert.Equal(t, "wal", journalMode)
	var busyTimeout int
	err = database.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout)
	require.NoError(t, err, "Failed to query busy_timeout")
	assert.Positive(t, busyTimeout, "Expected a non-zero busy timeout")

	// Test adding a link
	url := "https://example.com"
	title := "Example Website"
	description := "This is an example website"
	body := "<body><p>Some peculiar text in the body</p></body>"
	id, err := database.AddLink(t.Context(), url, title, description, []byte(body))
	require.NoError(t, err, "Failed to add link")
	assert.Positive(t, id, "Got %d, expected positive ID", id)

	// Test adding another link
	url2 := "https://other.com"
	title2 := "Fun page"
	description2 := "Here some completely different content"
	body2 := "<body><p>Other body data</p></body>"
	id2, err := database.AddLink(t.Context(), url2, title2, description2, []byte(body2))
	require.NoError(t, err, "Failed to add link 2")
	assert.Positive(t, id2, "Got %d, expected positive ID", id)
	assert.NotEqual(t, id, id2, "Expected different id")

	// Test adding a link without a body
	url3 := "https://empty.com"
	title3 := "PDF document"
	description3 := "application/pdf"
	id3, err := database.AddLink(t.Context(), url3, title3, description3, nil)
	require.NoError(t, err, "Failed to add link 3")
	assert.Positive(t, id3, "Got %d, expected positive ID", id)
	assert.NotEqual(t, id, id3, "Expected different id")
	assert.NotEqual(t, id2, id3, "Expected different id")

	// Test adding duplicate link
	_, err = database.AddLink(t.Context(), url, "bogus", "", nil)
	assert.ErrorIs(t, err, ErrDuplicate, "Expected error adding duplicate link")

	// Test getting all links
	links, err := database.GetAllLinks(t.Context())
	require.NoError(t, err, "Failed to get links")
	assert.Len(t, links, 3, "Got %d links, expected 3", len(links))
	assert.Equal(t, url, links[0].URL)
	assert.Equal(t, title, links[0].Title)
	assert.Equal(t, description, links[0].Description)
	assert.False(t, links[0].AddedAt.IsZero(), "Expected non-zero AddedAt")

	assert.Equal(t, url2, links[1].URL)
	assert.Equal(t, title2, links[1].Title)
	assert.Equal(t, description2, links[1].Description)
	assert.False(t, links[1].AddedAt.IsZero(), "Expected non-zero AddedAt")

	// Test search
	linksSearch, err := database.Search(t.Context(), "peculiar")
	require.NoError(t, err, "Failed to search")
	assert.Len(t, linksSearch, 1, "Got %d links, expected 1", len(linksSearch))
	assert.Equal(t, url, linksSearch[0].URL)
	assert.Equal(t, title, linksSearch[0].Title)
	assert.Equal(t, description, linksSearch[0].Description)
	assert.False(t, linksSearch[0].AddedAt.IsZero(), "Expected single non-zero AddedAt")

	// Test successful retrieval
	link, err := database.GetLink(t.Context(), id)
	assert.NoError(t, err, "Failed to get link")
	assert.Equal(t, url, link.URL)
	assert.Equal(t, title, link.Title)
	assert.Equal(t, description, link.Description)
	assert.False(t, link.AddedAt.IsZero(), "Expected single non-zero AddedAt")

	// Test non-existent link
	_, err = database.GetLink(t.Context(), 99999)
	assert.ErrorIs(t, err, ErrNotFound, "Got %v, expected ErrNotFound for fetching non-existent link", err)

	// Test updating a link
	err = database.UpdateLink(t.Context(), id, "Updated title", "Updated description", nil)
	require.NoError(t, err, "Failed to update link")
	link, err = database.GetLink(t.Context(), id)
	assert.NoError(t, err, "Failed to get updated link")
	assert.Equal(t, "Updated title", link.Title)
	assert.Equal(t, "Updated description", link.Description)

	// The FTS index must follow the edit: the new text is searchable, the
	// replaced text is not, and the body is still matched.
	linksSearch, err = database.Search(t.Context(), "Updated")
	require.NoError(t, err, "Failed to search for updated link")
	assert.Len(t, linksSearch, 1, "Got %d links, expected the updated link", len(linksSearch))
	assert.Equal(t, "Updated title", linksSearch[0].Title)

	linksSearch, err = database.Search(t.Context(), "Example")
	require.NoError(t, err, "Failed to search for replaced title")
	assert.Empty(t, linksSearch, "Expected the replaced title to be gone from the index")

	linksSearch, err = database.Search(t.Context(), "peculiar")
	require.NoError(t, err, "Failed to search body after update")
	assert.Len(t, linksSearch, 1, "Got %d links, expected the body to still be indexed", len(linksSearch))
	assert.Equal(t, "Updated title", linksSearch[0].Title)

	// Updating a link without a body must not fail.
	err = database.UpdateLink(t.Context(), id3, "Updated PDF", "application/pdf", nil)
	require.NoError(t, err, "Failed to update link without a body")
	linksSearch, err = database.Search(t.Context(), "Updated")
	require.NoError(t, err, "Failed to search after updating link without a body")
	assert.Len(t, linksSearch, 2, "Got %d links, expected both updated links", len(linksSearch))

	// Test updating a non-existing link
	err = database.UpdateLink(t.Context(), 9999, "Nothing", "Nothing", nil)
	assert.ErrorIs(t, err, ErrNotFound, "Got %v, expected ErrNotFound for updating non-existent link", err)

	// Test which links have a body stored, so that a caller knows when a
	// refetch is worthwhile
	hasBody, err := database.HasBody(t.Context(), id)
	require.NoError(t, err, "Failed to check for a body")
	assert.True(t, hasBody, "Expected a body for a link added with one")
	hasBody, err = database.HasBody(t.Context(), id3)
	require.NoError(t, err, "Failed to check for a missing body")
	assert.False(t, hasBody, "Expected no body for a link added without one")
	_, err = database.HasBody(t.Context(), 9999)
	assert.ErrorIs(t, err, ErrNotFound, "Got %v, expected ErrNotFound for a non-existent link", err)

	// Test supplying a body on update, as a refetch does
	err = database.UpdateLink(t.Context(), id3, "Updated PDF", "application/pdf", []byte("Refetched singular content"))
	require.NoError(t, err, "Failed to update link with a refetched body")
	hasBody, err = database.HasBody(t.Context(), id3)
	require.NoError(t, err, "Failed to check for the refetched body")
	assert.True(t, hasBody, "Expected the refetched body to be stored")
	linksSearch, err = database.Search(t.Context(), "singular")
	require.NoError(t, err, "Failed to search for the refetched body")
	assert.Len(t, linksSearch, 1, "Got %d links, expected the refetched body to be indexed", len(linksSearch))
	assert.Equal(t, url3, linksSearch[0].URL)

	// A body supplied again replaces the stored one
	err = database.UpdateLink(t.Context(), id3, "Updated PDF", "application/pdf", []byte("Replaced idiosyncratic content"))
	require.NoError(t, err, "Failed to replace the body")
	linksSearch, err = database.Search(t.Context(), "idiosyncratic")
	require.NoError(t, err, "Failed to search for the replaced body")
	assert.Len(t, linksSearch, 1, "Got %d links, expected the new body to be indexed", len(linksSearch))
	linksSearch, err = database.Search(t.Context(), "singular")
	require.NoError(t, err, "Failed to search for the previous body")
	assert.Empty(t, linksSearch, "Expected the previous body to be gone from the index")

	// A later update without a body carries the stored one forward
	err = database.UpdateLink(t.Context(), id3, "Updated PDF again", "application/pdf", nil)
	require.NoError(t, err, "Failed to update link after the refetch")
	linksSearch, err = database.Search(t.Context(), "idiosyncratic")
	require.NoError(t, err, "Failed to search for the carried over body")
	assert.Len(t, linksSearch, 1, "Got %d links, expected the body to be carried over", len(linksSearch))

	// Test deleting a link
	err = database.DeleteLink(t.Context(), id)
	require.NoError(t, err, "Failed to delete link")

	// Test deleting a non-existing link
	err = database.DeleteLink(t.Context(), 9999)
	assert.ErrorIs(t, err, ErrNotFound, "Got %v, expected ErrNotFound for deleting non-existent link", err)

	// Verify the link was deleted
	links, err = database.GetAllLinks(t.Context())
	require.NoError(t, err, "Failed to get links after deletion")
	assert.Len(t, links, 2, "Got %d links after deletion, expected 2", len(links))

	// Verify the body was deleted along with the link
	var bodies int
	err = database.QueryRow("SELECT count(*) FROM link_bodies WHERE link_id = ?", id).Scan(&bodies)
	require.NoError(t, err, "Failed to count bodies")
	assert.Zero(t, bodies, "Expected the body to be deleted along with the link")
	linksSearch, err = database.Search(t.Context(), "peculiar")
	require.NoError(t, err, "Failed to search after deletion")
	assert.Empty(t, linksSearch, "Expected the deleted link to be gone from the index")

	// Close the database
	err = database.Close()
	require.NoError(t, err, "Failed to close database")

	// Make the database file read-only
	err = os.Chmod(dbFile, 0400)
	require.NoError(t, err)

	// Attempt to open the database again - should fail
	database, err = InitDB(dbFile)
	assert.Error(t, err, "Unable to detect read-only database")
	if err == nil {
		_ = database.Close()
	}
}

// TestConcurrentAccess verifies that concurrent readers and writers do not
// fail with SQLITE_BUSY.
func TestConcurrentAccess(t *testing.T) {
	dbFile := filepath.Join(t.TempDir(), "test_concurrent.database")

	database, err := InitDB(dbFile)
	require.NoError(t, err, "Failed to initialize database")
	t.Cleanup(func() {
		_ = database.Close()
	})

	const writers = 8
	const readers = 8
	const iterations = 20

	var wg sync.WaitGroup
	// Buffered for the maximum number of errors, so no goroutine blocks on send
	errs := make(chan error, (writers+2*readers)*iterations)

	for w := range writers {
		wg.Go(func() {
			for i := range iterations {
				url := fmt.Sprintf("https://example.com/%d/%d", w, i)
				if _, err := database.AddLink(t.Context(), url, "Example Website", "An example", []byte("body text")); err != nil {
					errs <- err
				}
			}
		})
	}

	for range readers {
		wg.Go(func() {
			for range iterations {
				if _, err := database.GetAllLinks(t.Context()); err != nil {
					errs <- err
				}
				if _, err := database.Search(t.Context(), "body"); err != nil {
					errs <- err
				}
			}
		})
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		assert.NoError(t, err, "Concurrent access failed")
	}

	links, err := database.GetAllLinks(t.Context())
	require.NoError(t, err, "Failed to get links")
	assert.Len(t, links, writers*iterations, "Got %d links, expected %d", len(links), writers*iterations)
}

// TestContextCancellation verifies that the database operations honor the
// context, so that a request which is aborted does not keep a connection busy.
func TestContextCancellation(t *testing.T) {
	dbFile := filepath.Join(t.TempDir(), "test_context.database")

	database, err := InitDB(dbFile)
	require.NoError(t, err, "Failed to initialize database")
	t.Cleanup(func() {
		_ = database.Close()
	})

	_, err = database.AddLink(t.Context(), "https://example.com", "Example Website", "An example", []byte("body text"))
	require.NoError(t, err, "Failed to add link")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err = database.AddLink(ctx, "https://other.com", "Fun page", "Something else", nil)
	assert.ErrorIs(t, err, context.Canceled, "AddLink ignored the context")

	_, err = database.GetAllLinks(ctx)
	assert.ErrorIs(t, err, context.Canceled, "GetAllLinks ignored the context")

	_, err = database.Search(ctx, "body")
	assert.ErrorIs(t, err, context.Canceled, "Search ignored the context")

	_, err = database.GetLink(ctx, 1)
	assert.ErrorIs(t, err, context.Canceled, "GetLink ignored the context")

	err = database.UpdateLink(ctx, 1, "Updated title", "Updated description", nil)
	assert.ErrorIs(t, err, context.Canceled, "UpdateLink ignored the context")

	err = database.DeleteLink(ctx, 1)
	assert.ErrorIs(t, err, context.Canceled, "DeleteLink ignored the context")

	// None of the cancelled operations should have changed anything
	links, err := database.GetAllLinks(t.Context())
	require.NoError(t, err, "Failed to get links")
	assert.Len(t, links, 1, "Got %d links, expected 1", len(links))
	assert.Equal(t, "Example Website", links[0].Title)
}

// TestInvalidDatabaseFile verifies that a file name which the driver would
// split into a DSN is rejected rather than silently opening another database.
func TestInvalidDatabaseFile(t *testing.T) {
	dbFile := filepath.Join(t.TempDir(), "test?_pragma=journal_mode(DELETE)")

	database, err := InitDB(dbFile)
	assert.Error(t, err, "Expected error for a database file name containing '?'")
	if err == nil {
		_ = database.Close()
	}
}
