package db

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

//goland:noinspection GoDirectComparisonOfErrors
func TestDB(t *testing.T) {
	// Use a temporary database file for testing
	dbFile := "test.database"

	// Initialize the database
	database, err := InitDB(dbFile)
	require.NoError(t, err, "Failed to initialize database")

	t.Cleanup(func() {
		if database != nil {
			_ = database.Close()
		}
		_ = os.Remove(dbFile)
	})

	// Test adding a link
	url := "https://example.com"
	title := "Example Website"
	description := "This is an example website"
	body := "<body><p>Some peculiar text in the body</p></body>"
	id, err := database.AddLink(url, title, description, []byte(body))
	require.NoError(t, err, "Failed to add link")
	assert.Positive(t, id, "Got %d, expected positive ID", id)

	// Test adding another link
	url2 := "https://other.com"
	title2 := "Fun page"
	description2 := "Here some completely different content"
	body2 := "<body><p>Other body data</p></body>"
	id2, err := database.AddLink(url2, title2, description2, []byte(body2))
	require.NoError(t, err, "Failed to add link 2")
	assert.Positive(t, id2, "Got %d, expected positive ID", id)
	assert.NotEqual(t, id, id2, "Expected different id")

	// Test adding a link without a body
	url3 := "https://empty.com"
	title3 := "PDF document"
	description3 := "application/pdf"
	id3, err := database.AddLink(url3, title3, description3, nil)
	require.NoError(t, err, "Failed to add link 3")
	assert.Positive(t, id3, "Got %d, expected positive ID", id)
	assert.NotEqual(t, id, id3, "Expected different id")
	assert.NotEqual(t, id2, id3, "Expected different id")

	// Test adding duplicate link
	_, err = database.AddLink(url, "bogus", "", nil)
	assert.ErrorIs(t, err, ErrDuplicate, "Expected error adding duplicate link")

	// Test getting all links
	links, err := database.GetAllLinks()
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
	linksSearch, err := database.Search("peculiar")
	require.NoError(t, err, "Failed to search")
	assert.Len(t, linksSearch, 1, "Got %d links, expected 1", len(linksSearch))
	assert.Equal(t, url, linksSearch[0].URL)
	assert.Equal(t, title, linksSearch[0].Title)
	assert.Equal(t, description, linksSearch[0].Description)
	assert.False(t, linksSearch[0].AddedAt.IsZero(), "Expected single non-zero AddedAt")

	// Test successful retrieval
	link, err := database.GetLink(id)
	assert.NoError(t, err, "Failed to get link")
	assert.Equal(t, url, link.URL)
	assert.Equal(t, title, link.Title)
	assert.Equal(t, description, link.Description)
	assert.False(t, link.AddedAt.IsZero(), "Expected single non-zero AddedAt")

	// Test non-existent link
	_, err = database.GetLink(99999)
	assert.ErrorIs(t, err, ErrNotFound, "Got %v, expected ErrNotFound for fetching non-existent link", err)

	// Test updating a link
	err = database.UpdateLink(id, "Updated title", "Updated description")
	require.NoError(t, err, "Failed to update link")
	link, err = database.GetLink(id)
	assert.NoError(t, err, "Failed to get updated link")
	assert.Equal(t, "Updated title", link.Title)
	assert.Equal(t, "Updated description", link.Description)

	// Test deleting a link
	err = database.DeleteLink(id)
	require.NoError(t, err, "Failed to delete link")

	// Test deleting a non-existing link
	err = database.DeleteLink(9999)
	assert.ErrorIs(t, err, ErrNotFound, "Got %v, expected ErrNotFound for deleting non-existent link", err)

	// Verify the link was deleted
	links, err = database.GetAllLinks()
	require.NoError(t, err, "Failed to get links after deletion")
	assert.Len(t, links, 2, "Got %d links after deletion, expected 2", len(links))

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
