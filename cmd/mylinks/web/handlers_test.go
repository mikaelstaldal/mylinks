package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mikaelstaldal/mylinks/cmd/mylinks/db"
)

func TestHandlers(t *testing.T) {
	// Use a temporary database file for testing
	dbFile := "test_handlers.database"

	testTitle := "Test Title"
	testDescription := "Test Description"

	// Initialize the database
	database, err := db.InitDB(dbFile)
	require.NoError(t, err, "Failed to initialize database")
	t.Cleanup(func() {
		_ = database.Close()
		_ = os.Remove(dbFile)
	})

	handler := newHandlers("../../..", database, "", true).Routes()

	// Create a mock HTTP server to simulate a valid URL
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "<html><head><title>%s</title><meta name='description' content='%s'></head><body>Some body</body></html>", testTitle, testDescription)
	}))
	defer mockServer.Close()

	var linkId int64

	t.Run("add link success", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/", strings.NewReader("url="+mockServer.URL))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response, body := testRequest(t, handler, req)

		assert.Equal(t, http.StatusCreated, response.StatusCode, "Handlers returned wrong status code")

		locationHeader := response.Header.Get("Location")
		linkIdString, found := strings.CutPrefix(locationHeader, "/")
		require.True(t, found, "Response Location header doesn't has correct format: '%s'", locationHeader)
		linkId, err = strconv.ParseInt(linkIdString, 10, 64)
		require.NoError(t, err, "Failed to convert link ID")

		assert.Contains(t, string(body), mockServer.URL, "Response doesn't contain the expected link URL")
		assert.Contains(t, string(body), testTitle, "Response doesn't contain the expected link title")
		assert.Contains(t, string(body), testDescription, "Response doesn't contain the expected link description")
	})

	t.Run("add link missing url", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/", strings.NewReader(""))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response, _ := testRequest(t, handler, req)

		assert.Equal(t, http.StatusBadRequest, response.StatusCode, "Handlers returned wrong status code")
	})

	t.Run("add link invalid url", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/", strings.NewReader("url=invalid-url"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response, _ := testRequest(t, handler, req)

		assert.Equal(t, http.StatusBadRequest, response.StatusCode, "Handlers returned wrong status code")
	})

	t.Run("get all links success", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		response, body := testRequest(t, handler, req)

		assert.Equal(t, http.StatusOK, response.StatusCode, "Handlers returned wrong status code")

		assert.True(t, strings.HasPrefix(response.Header.Get("Content-Type"), "text/html"), "Wrong Content-Type: %s", response.Header.Get("Content-Type"))

		assert.Contains(t, string(body), mockServer.URL, "Response doesn't contain the expected link URL")
		assert.Contains(t, string(body), testTitle, "Response doesn't contain the expected link title")
		assert.Contains(t, string(body), testDescription, "Response doesn't contain the expected link description")
		assert.Contains(t, string(body), time.Now().Format("2006-01-02 "), "Response doesn't contain the expected date")
	})

	t.Run("get all links as JSON", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Accept", "application/json")
		response, body := testRequest(t, handler, req)

		assert.Equal(t, http.StatusOK, response.StatusCode, "Handlers returned wrong status code")

		assert.True(t, strings.HasPrefix(response.Header.Get("Content-Type"), "application/json"), "Wrong Content-Type: %s", response.Header.Get("Content-Type"))

		var data []db.Link
		err := json.Unmarshal(body, &data)
		assert.NoError(t, err, "Response doesn't contain the expected JSON")
		assert.Len(t, data, 1, "Wrong length of JSON response")
		if len(data) == 1 {
			assert.Equal(t, mockServer.URL, data[0].URL)
			assert.Equal(t, testTitle, data[0].Title)
			assert.Equal(t, testDescription, data[0].Description)
		}
	})

	t.Run("search success", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/?s=test", nil)
		response, body := testRequest(t, handler, req)

		assert.Equal(t, http.StatusOK, response.StatusCode, "Handlers returned wrong status code")

		assert.True(t, strings.HasPrefix(response.Header.Get("Content-Type"), "text/html"), "Wrong Content-Type: %s", response.Header.Get("Content-Type"))

		assert.Contains(t, string(body), mockServer.URL, "Response doesn't contain the expected link URL")
		assert.Contains(t, string(body), testTitle, "Response doesn't contain the expected link title")
		assert.Contains(t, string(body), testDescription, "Response doesn't contain the expected link description")
		assert.Contains(t, string(body), time.Now().Format("2006-01-02 "), "Response doesn't contain the expected date")
	})

	t.Run("get single link success", func(t *testing.T) {
		req := httptest.NewRequest("GET", fmt.Sprintf("/%d", linkId), nil)
		response, body := testRequest(t, handler, req)

		assert.Equal(t, http.StatusOK, response.StatusCode, "Handlers returned wrong status code")

		assert.True(t, strings.HasPrefix(response.Header.Get("Content-Type"), "text/html"), "Wrong Content-Type: %s", response.Header.Get("Content-Type"))

		assert.Contains(t, string(body), mockServer.URL, "Response doesn't contain the expected link URL")
		assert.Contains(t, string(body), testTitle, "Response doesn't contain expected title")
		assert.Contains(t, string(body), testDescription, "Response doesn't contain the expected link description")
		assert.NotContains(t, string(body), "class=\"link-edit\"", "Response contain the unexpected edit form")
	})

	t.Run("get single link for edit success", func(t *testing.T) {
		req := httptest.NewRequest("GET", fmt.Sprintf("/%d?edit=1", linkId), nil)
		response, body := testRequest(t, handler, req)

		assert.Equal(t, http.StatusOK, response.StatusCode, "Handlers returned wrong status code")

		assert.True(t, strings.HasPrefix(response.Header.Get("Content-Type"), "text/html"), "Wrong Content-Type: %s", response.Header.Get("Content-Type"))

		assert.Contains(t, string(body), mockServer.URL, "Response doesn't contain the expected link URL")
		assert.Contains(t, string(body), testTitle, "Response doesn't contain expected title")
		assert.Contains(t, string(body), testDescription, "Response doesn't contain the expected link description")
		assert.Contains(t, string(body), "class=\"link-edit\"", "Response doesn't contain the expected edit form")
	})

	t.Run("get single link as JSON", func(t *testing.T) {
		req := httptest.NewRequest("GET", fmt.Sprintf("/%d", linkId), nil)
		req.Header.Set("Accept", "application/json")
		response, body := testRequest(t, handler, req)

		assert.Equal(t, http.StatusOK, response.StatusCode, "Handlers returned wrong status code")

		assert.True(t, strings.HasPrefix(response.Header.Get("Content-Type"), "application/json"), "Wrong Content-Type: %s", response.Header.Get("Content-Type"))

		var data db.Link
		err := json.Unmarshal(body, &data)
		assert.NoError(t, err, "Response doesn't contain the expected JSON")
		assert.Equal(t, mockServer.URL, data.URL)
		assert.Equal(t, testTitle, data.Title)
		assert.Equal(t, testDescription, data.Description)
	})

	t.Run("get single link invalid id", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/invalid", nil)
		response, _ := testRequest(t, handler, req)

		assert.Equal(t, http.StatusBadRequest, response.StatusCode, "Handlers returned wrong status code")
	})

	t.Run("get single link not found", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/999", nil)
		response, _ := testRequest(t, handler, req)

		assert.Equal(t, http.StatusNotFound, response.StatusCode, "Handlers returned wrong status code")
	})

	t.Run("patch link success", func(t *testing.T) {
		req := httptest.NewRequest("PATCH", fmt.Sprintf("/%d", linkId), strings.NewReader("title=Updated Title&description=Updated Description"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response, body := testRequest(t, handler, req)

		assert.Equal(t, http.StatusOK, response.StatusCode, "Handlers returned wrong status code")

		assert.True(t, strings.HasPrefix(response.Header.Get("Content-Type"), "text/html"), "Wrong Content-Type: %s", response.Header.Get("Content-Type"))

		assert.Contains(t, string(body), "Updated Title", "Response doesn't contain the updated title")

		assert.Contains(t, string(body), "Updated Description", "Response doesn't contain the updated description")

		// Verify the link was actually updated in the database
		updatedLink, err := database.GetLink(linkId)
		require.NoError(t, err, "Failed to get updated link")
		assert.Equal(t, "Updated Title", updatedLink.Title, "Link title was not updated in database")
		assert.Equal(t, "Updated Description", updatedLink.Description, "Link description was not updated in database")
	})

	t.Run("patch link success JSON", func(t *testing.T) {
		req := httptest.NewRequest("PATCH", fmt.Sprintf("/%d", linkId), strings.NewReader("title=Updated Title&description=Updated Description"))
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response, body := testRequest(t, handler, req)

		assert.Equal(t, http.StatusOK, response.StatusCode, "Handlers returned wrong status code")

		assert.True(t, strings.HasPrefix(response.Header.Get("Content-Type"), "application/json"), "Wrong Content-Type: %s", response.Header.Get("Content-Type"))

		var data db.Link
		err := json.Unmarshal(body, &data)
		assert.NoError(t, err, "Response doesn't contain the expected JSON")
		assert.Equal(t, mockServer.URL, data.URL)
		assert.Equal(t, "Updated Title", data.Title)
		assert.Equal(t, "Updated Description", data.Description)

		// Verify the link was actually updated in the database
		updatedLink, err := database.GetLink(linkId)
		require.NoError(t, err, "Failed to get updated link")
		assert.Equal(t, "Updated Title", updatedLink.Title, "Link title was not updated in database")
		assert.Equal(t, "Updated Description", updatedLink.Description, "Link description was not updated in database")
	})

	t.Run("patch link invalid id", func(t *testing.T) {
		req := httptest.NewRequest("PATCH", "/invalid", strings.NewReader("title=Updated Title&description=Updated Description"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response, _ := testRequest(t, handler, req)

		assert.Equal(t, http.StatusBadRequest, response.StatusCode, "Handlers returned wrong status code")
	})

	t.Run("patch link not found", func(t *testing.T) {
		req := httptest.NewRequest("PATCH", "/999", strings.NewReader("title=Updated Title&description=Updated Description"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response, _ := testRequest(t, handler, req)

		assert.Equal(t, http.StatusNotFound, response.StatusCode, "Handlers returned wrong status code")
	})

	t.Run("patch link missing title", func(t *testing.T) {
		req := httptest.NewRequest("PATCH", fmt.Sprintf("/%d", linkId), strings.NewReader("description=Updated Description"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response, _ := testRequest(t, handler, req)

		assert.Equal(t, http.StatusBadRequest, response.StatusCode, "Handlers returned wrong status code")
	})

	t.Run("patch link too long title", func(t *testing.T) {
		req := httptest.NewRequest("PATCH", fmt.Sprintf("/%d", linkId), strings.NewReader("title="+strings.Repeat("a", maxTitleLength+1)))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response, _ := testRequest(t, handler, req)

		assert.Equal(t, http.StatusBadRequest, response.StatusCode, "Handlers returned wrong status code")
	})

	t.Run("patch link too long description", func(t *testing.T) {
		req := httptest.NewRequest("PATCH", fmt.Sprintf("/%d", linkId), strings.NewReader("title=whatever&description="+strings.Repeat("b", maxDescriptionLength+1)))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response, _ := testRequest(t, handler, req)

		assert.Equal(t, http.StatusBadRequest, response.StatusCode, "Handlers returned wrong status code")
	})

	t.Run("delete link success", func(t *testing.T) {
		req := httptest.NewRequest("DELETE", fmt.Sprintf("/%d", linkId), nil)
		response, _ := testRequest(t, handler, req)

		assert.Equal(t, http.StatusOK, response.StatusCode, "Handlers returned wrong status code")

		// Verify link was deleted
		_, err = database.GetLink(1)
		assert.Error(t, err, "Link should have been deleted")
	})

	t.Run("delete link invalid id", func(t *testing.T) {
		req := httptest.NewRequest("DELETE", "/invalid", nil)
		response, _ := testRequest(t, handler, req)

		assert.Equal(t, http.StatusBadRequest, response.StatusCode, "Handlers returned wrong status code")
	})

	t.Run("delete link not found", func(t *testing.T) {
		req := httptest.NewRequest("DELETE", "/999", nil)
		response, _ := testRequest(t, handler, req)

		assert.Equal(t, http.StatusNotFound, response.StatusCode, "Handlers returned wrong status code")
	})

	t.Run("add note success", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/", strings.NewReader("note-title=NoteTitle&note-text=NoteText"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response, body := testRequest(t, handler, req)

		assert.Equal(t, http.StatusCreated, response.StatusCode, "Handlers returned wrong status code")

		locationHeader := response.Header.Get("Location")
		linkIdString, found := strings.CutPrefix(locationHeader, "/")
		require.True(t, found, "Response Location header doesn't has correct format: '%s'", locationHeader)
		linkId, err = strconv.ParseInt(linkIdString, 10, 64)
		require.NoError(t, err, "Failed to convert note ID")

		assert.Contains(t, string(body), "NoteTitle", "Response doesn't contain the expected note title")
		assert.Contains(t, string(body), "NoteText", "Response doesn't contain the expected note text")
	})

	t.Run("add note missing text", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/", strings.NewReader("note-title=NoteTitle&note-text="))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response, _ := testRequest(t, handler, req)

		assert.Equal(t, http.StatusBadRequest, response.StatusCode, "Handlers returned wrong status code")
	})

	t.Run("get single note success", func(t *testing.T) {
		req := httptest.NewRequest("GET", fmt.Sprintf("/%d", linkId), nil)
		response, body := testRequest(t, handler, req)

		assert.Equal(t, http.StatusOK, response.StatusCode, "Handlers returned wrong status code")

		assert.True(t, strings.HasPrefix(response.Header.Get("Content-Type"), "text/html"), "Wrong Content-Type: %s", response.Header.Get("Content-Type"))

		assert.Contains(t, string(body), "NoteTitle", "Response doesn't contain expected title")
		assert.Contains(t, string(body), "NoteText", "Response doesn't contain the expected note")
	})

	t.Run("get single note as JSON", func(t *testing.T) {
		req := httptest.NewRequest("GET", fmt.Sprintf("/%d", linkId), nil)
		req.Header.Set("Accept", "application/json")
		response, body := testRequest(t, handler, req)

		assert.Equal(t, http.StatusOK, response.StatusCode, "Handlers returned wrong status code")

		assert.True(t, strings.HasPrefix(response.Header.Get("Content-Type"), "application/json"), "Wrong Content-Type: %s", response.Header.Get("Content-Type"))

		var data db.Link
		err := json.Unmarshal(body, &data)
		assert.NoError(t, err, "Response doesn't contain the expected JSON")
		assert.True(t, strings.HasPrefix(data.URL, "note:"), "Response doesn't contain the expected note URL")
		assert.Equal(t, "NoteTitle", data.Title)
		assert.Equal(t, "NoteText", data.Description)
	})

	t.Run("bookmarklet save success", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/bookmarklet?url="+url.QueryEscape(mockServer.URL+"/bookmarklet-page"), nil)
		response, body := testRequest(t, handler, req)

		assert.Equal(t, http.StatusCreated, response.StatusCode, "Handlers returned wrong status code")

		assert.Contains(t, string(body), "Link saved!", "Response doesn't contain success message")
	})

	t.Run("bookmarklet save missing url", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/bookmarklet", nil)
		response, _ := testRequest(t, handler, req)

		assert.Equal(t, http.StatusBadRequest, response.StatusCode, "Handlers returned wrong status code")
	})

	t.Run("bookmarklet save invalid url", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/bookmarklet?url=not-a-valid-url", nil)
		response, _ := testRequest(t, handler, req)

		assert.Equal(t, http.StatusBadRequest, response.StatusCode, "Handlers returned wrong status code")
	})

	t.Run("bookmarklet save duplicate url", func(t *testing.T) {
		// Save the same URL again - should get conflict
		req := httptest.NewRequest("GET", "/bookmarklet?url="+url.QueryEscape(mockServer.URL+"/bookmarklet-page"), nil)
		response, _ := testRequest(t, handler, req)

		assert.Equal(t, http.StatusConflict, response.StatusCode, "Handlers returned wrong status code")
	})

	t.Run("main page contains bookmarklet link", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		response, body := testRequest(t, handler, req)

		assert.Equal(t, http.StatusOK, response.StatusCode, "Handlers returned wrong status code")

		assert.Contains(t, string(body), "Save to MyLinks", "Response doesn't contain bookmarklet link")
	})
}

func testRequest(t *testing.T, handler http.Handler, req *http.Request) (*http.Response, []byte) {
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	result := rr.Result()
	body, err := io.ReadAll(result.Body)
	require.NoError(t, err, "Failed to read response body")
	_ = result.Body.Close()
	return result, body
}

func Test_extractTitleAndDescriptionAndBodyFromURL(t *testing.T) {
	handlers := newHandlers("../../..", nil, "", true)

	tests := []struct {
		name         string
		contentType  string
		returnedBody []byte
		title        string
		description  string
		body         []byte
		wantErr      bool
	}{
		{
			name:         "valid HTML page",
			contentType:  "text/html",
			returnedBody: []byte("<html><head><title>Example Domain</title><meta name='description' content='This domain is for use in illustrative examples in documents.'></head><body>\n<div>\n<h1>Some header</h1>\n</div>\n</body></html>"),
			title:        "Example Domain",
			description:  "This domain is for use in illustrative examples in documents.",
			body:         []byte("<body>\n<div>\n<h1>Some header</h1>\n</div>\n</body>"),
			wantErr:      false,
		},
		{
			name:         "Invalid PDF",
			contentType:  "application/pdf",
			returnedBody: []byte("invalid pdf"),
			title:        "server.URL",
			description:  "PDF",
			body:         nil,
			wantErr:      false,
		},
		{
			name:         "Other content",
			contentType:  "image/jpeg",
			returnedBody: []byte("binary data"),
			title:        "server.URL",
			description:  "image/jpeg",
			body:         nil,
			wantErr:      false,
		},
		{
			name:         "no title found",
			contentType:  "text/html",
			returnedBody: []byte("<html><head><meta name='description' content='This domain is for use in illustrative examples in documents.'></head><body>\n<div>\n<h1>Some header</h1>\n</div>\n</body></html"),
			title:        "",
			description:  "",
			body:         nil,
			wantErr:      true,
		},
		{
			name:         "very long title",
			contentType:  "text/html",
			returnedBody: []byte("<html><head><title>" + strings.Repeat("a", maxTitleLength+100) + "</title><meta name='description' content='This domain is for use in illustrative examples in documents.'></head><body>\n<div>\n<h1>Some header</h1>\n</div>\n</body></html"),
			title:        strings.Repeat("a", maxTitleLength) + "...",
			description:  "This domain is for use in illustrative examples in documents.",
			body:         []byte("<body>\n<div>\n<h1>Some header</h1>\n</div>\n</body>"),
			wantErr:      false,
		},
		{
			name:         "very long description",
			contentType:  "text/html",
			returnedBody: []byte("<html><head><title>Example Domain</title><meta name='description' content='" + strings.Repeat("b", maxDescriptionLength+100) + "'></head><body>\n<div>\n<h1>Some header</h1>\n</div>\n</body></html"),
			title:        "Example Domain",
			description:  strings.Repeat("b", maxDescriptionLength) + "...",
			body:         []byte("<body>\n<div>\n<h1>Some header</h1>\n</div>\n</body>"),
			wantErr:      false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tt.contentType)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(tt.returnedBody)
			}))
			defer server.Close()

			parsedURL, _ := url.Parse(server.URL)
			title, description, body, err := handlers.extractTitleAndDescriptionAndBodyFromURL(parsedURL)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			if tt.title == "server.URL" {
				assert.True(t, strings.HasSuffix(server.URL, title), "extractTitleAndDescriptionAndBodyFromURL() title = '%v', title '%v'", title, server.URL)
			} else {
				assert.Equal(t, tt.title, title)
			}
			assert.Equal(t, tt.description, description)
			assert.True(t, bytes.HasPrefix(body, tt.body), "extractTitleAndDescriptionAndBodyFromURL() body = '%v', body '%v'", string(body), string(tt.body))
		})
	}
}

func Test_extractTitleFromURL(t *testing.T) {
	handlers := newHandlers("../../..", nil, "", true)

	tests := []struct {
		name     string
		rawURL   string
		expected string
		hasError bool
	}{
		{
			name:     "URL with trailing path segment",
			rawURL:   "http://example.com/some/path",
			expected: "path",
			hasError: false,
		},
		{
			name:     "URL with single segment",
			rawURL:   "http://example.com/single",
			expected: "single",
			hasError: false,
		},
		{
			name:     "Root URL with trailing slash",
			rawURL:   "http://example.com/",
			expected: "example.com",
			hasError: false,
		},
		{
			name:     "Root URL without slash",
			rawURL:   "http://example.com",
			expected: "example.com",
			hasError: false,
		},
		{
			name:     "URL with query parameters",
			rawURL:   "http://example.com/path/to/page?query=1",
			expected: "page",
			hasError: false,
		},
		{
			name:     "URL with special characters in path segment",
			rawURL:   "http://example.com/path/to/chapter_1-2",
			expected: "chapter_1-2",
			hasError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			urlParsed, err := url.Parse(tt.rawURL)
			require.NoError(t, err)
			title := handlers.extractTitleFromURL(urlParsed)
			assert.Equal(t, tt.expected, title)
		})
	}
}
