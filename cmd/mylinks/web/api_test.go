package web

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mikaelstaldal/mylinks/cmd/mylinks/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func apiTestHandlers(t *testing.T) *Handlers {
	t.Helper()
	database, err := db.InitDB(filepath.Join(t.TempDir(), "api.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	_, err = database.AddLink(t.Context(), "note:1", "Original title", "Original text", []byte("Original text"))
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "UPDATE links SET added_at = '2026-01-01 00:00:00'")
	require.NoError(t, err)
	return newHandlers("../../..", database, "", true)
}

// Keep the pre-generation router here as a compatibility oracle for wire
// details that OpenAPI cannot express (notably exact Accept matching).
func originalAPIRoutes(h *Handlers) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", h.ListLinks)
	mux.HandleFunc("POST /{$}", h.AddItem)
	mux.HandleFunc("GET /{id}", h.GetLink)
	mux.HandleFunc("PATCH /{id}", h.EditLink)
	mux.HandleFunc("DELETE /{id}", h.DeleteLink)
	mux.HandleFunc("GET /bookmarklet", h.BookmarkletSave)
	return commonHeaders(mux)
}

func TestGeneratedAPICompatibility(t *testing.T) {
	tests := []struct {
		name, method, target, body, contentType string
		accept                                  []string
		hx                                      string
	}{
		{name: "list JSON", method: "GET", target: "/", accept: []string{"application/json"}},
		{name: "case insensitive JSON", method: "GET", target: "/", accept: []string{"APPLICATION/JSON"}},
		{name: "multiple Accept headers", method: "GET", target: "/", accept: []string{"text/html", "application/json"}},
		{name: "Accept list is HTML", method: "GET", target: "/", accept: []string{"application/json,text/html"}},
		{name: "Accept quality is HTML", method: "GET", target: "/", accept: []string{"application/json;q=1"}},
		{name: "Accept whitespace is HTML", method: "GET", target: "/", accept: []string{" application/json "}},
		{name: "list page", method: "GET", target: "/"},
		{name: "list fragment", method: "GET", target: "/", hx: "true"},
		{name: "search", method: "GET", target: "/?s=Original", accept: []string{"application/json"}},
		{name: "malformed query", method: "GET", target: "/?s=%zz", accept: []string{"application/json"}},
		{name: "item JSON", method: "GET", target: "/1", accept: []string{"application/json"}},
		{name: "item fragment", method: "GET", target: "/1"},
		{name: "edit form", method: "GET", target: "/1?edit=1"},
		{name: "invalid ID", method: "GET", target: "/invalid"},
		{name: "overflow ID", method: "GET", target: "/9999999999999999999999"},
		{name: "missing item", method: "GET", target: "/1000"},
		{name: "edit JSON", method: "PATCH", target: "/1", body: "title=Edited&description=New", contentType: "application/x-www-form-urlencoded", accept: []string{"application/json"}},
		{name: "edit HTML", method: "PATCH", target: "/1?edit=1", body: "title=Edited", contentType: "application/x-www-form-urlencoded"},
		{name: "repeated fields", method: "PATCH", target: "/1", body: "title=First&title=Second", contentType: "application/x-www-form-urlencoded"},
		{name: "malformed form", method: "PATCH", target: "/1", body: "title=%zz", contentType: "application/x-www-form-urlencoded"},
		{name: "missing edit title", method: "PATCH", target: "/1", body: "description=New", contentType: "application/x-www-form-urlencoded"},
		{name: "unsupported edit content type", method: "PATCH", target: "/1", body: "{}", contentType: "application/json"},
		{name: "invalid ID before form error", method: "PATCH", target: "/bad", body: "title=%zz", contentType: "application/x-www-form-urlencoded"},
		{name: "missing edited item", method: "PATCH", target: "/1000", body: "title=Edited", contentType: "application/x-www-form-urlencoded"},
		{name: "empty POST", method: "POST", target: "/"},
		{name: "malformed POST", method: "POST", target: "/", body: "note-title=%zz", contentType: "application/x-www-form-urlencoded"},
		{name: "missing note text", method: "POST", target: "/", body: "note-title=Title", contentType: "application/x-www-form-urlencoded"},
		{name: "delete", method: "DELETE", target: "/1"},
		{name: "missing deletion", method: "DELETE", target: "/1000"},
		{name: "invalid deletion", method: "DELETE", target: "/bad"},
		{name: "bookmarklet missing URL", method: "GET", target: "/bookmarklet", accept: []string{"application/json"}},
		{name: "HEAD list", method: "HEAD", target: "/"},
		{name: "HEAD item", method: "HEAD", target: "/1"},
		{name: "wrong method", method: "PUT", target: "/1"},
		{name: "OPTIONS", method: "OPTIONS", target: "/"},
		{name: "static path with item method", method: "PATCH", target: "/bookmarklet", body: "title=Edited", contentType: "application/x-www-form-urlencoded"},
		{name: "unknown nested path", method: "GET", target: "/1/extra"},
		{name: "escaped ID", method: "GET", target: "/%31"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var responses []*httptest.ResponseRecorder
			for _, generated := range []bool{false, true} {
				h := apiTestHandlers(t)
				router := originalAPIRoutes(h)
				if generated {
					router = h.Routes()
				}
				r := httptest.NewRequest(tt.method, tt.target, strings.NewReader(tt.body))
				if tt.contentType != "" {
					r.Header.Set("Content-Type", tt.contentType)
				}
				for _, accept := range tt.accept {
					r.Header.Add("Accept", accept)
				}
				r.Header.Set("HX-Request", tt.hx)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				responses = append(responses, w)
			}
			assert.Equal(t, responses[0].Code, responses[1].Code)
			assert.Equal(t, responses[0].Header(), responses[1].Header())
			assert.Equal(t, responses[0].Body.String(), responses[1].Body.String())
		})
	}
}

// Read the document as well as exercising the server, so a newly documented
// route cannot silently miss this contract test.
func TestDocumentedAPIRoutes(t *testing.T) {
	specBytes, err := os.ReadFile("../../../openapi.yaml")
	require.NoError(t, err)
	var spec struct {
		OpenAPI string                    `yaml:"openapi"`
		Paths   map[string]map[string]any `yaml:"paths"`
	}
	require.NoError(t, yaml.Unmarshal(specBytes, &spec))
	require.Equal(t, "3.0.3", spec.OpenAPI)
	h := apiTestHandlers(t)
	router := h.Routes()
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, "<html><head><title>Saved page</title></head><body>Content</body></html>")
	}))
	defer page.Close()
	covered := make(map[string]bool)
	for _, tt := range []struct {
		method, path, target, body, accept, hx, contentType, contains string
		status                                                        int
	}{
		{"GET", "/", "/?s=Original", "", "application/json", "", "application/json", `"Title":"Original title"`, 200},
		{"GET", "/", "/", "", "", "", "text/html", "<!DOCTYPE html>", 200},
		{"GET", "/", "/", "", "", "true", "text/html", "Original title", 200},
		{"POST", "/", "/", "url=" + url.QueryEscape(page.URL), "application/json", "", "application/json", "Saved page", 201},
		{"GET", "/{id}", "/1", "", "application/json", "", "application/json", `"ID":1`, 200},
		{"GET", "/{id}", "/1?edit=1", "", "", "", "text/html", "Original title", 200},
		{"PATCH", "/{id}", "/1", "title=Changed&description=Edited", "application/json", "", "application/json", `"Title":"Changed"`, 200},
		{"DELETE", "/{id}", "/1", "", "", "", "", "", 200},
		{"GET", "/bookmarklet", "/bookmarklet?url=" + url.QueryEscape(page.URL+"/second"), "", "application/json", "true", "text/html", "<!DOCTYPE html>", 201},
	} {
		r := httptest.NewRequest(tt.method, tt.target, strings.NewReader(tt.body))
		if tt.body != "" {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		r.Header.Set("Accept", tt.accept)
		r.Header.Set("HX-Request", tt.hx)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		require.Equal(t, tt.status, w.Code, "%s %s", tt.method, tt.target)
		assert.Equal(t, tt.contentType, w.Header().Get("Content-Type"))
		if tt.contains != "" {
			assert.Contains(t, w.Body.String(), tt.contains)
		} else {
			assert.Empty(t, w.Body.String())
		}
		if tt.method == "POST" {
			assert.Equal(t, "/2", w.Header().Get("Location"))
		}
		covered[strings.ToLower(tt.method)+" "+tt.path] = true
	}
	for path, operations := range spec.Paths {
		for method := range operations {
			if method == "parameters" {
				continue
			}
			assert.True(t, covered[method+" "+path], "undocumented test coverage for %s %s", method, path)
		}
	}
}

// A decoder must not erase an I/O failure by consuming it before ParseForm.
type failingAPIReader struct{ first bool }

func (r *failingAPIReader) Read(p []byte) (int, error) {
	if r.first {
		return 0, io.EOF
	}
	r.first = true
	return copy(p, "title=Changed"), errors.New("body read failed")
}

func TestGeneratedAPIFormReadFailures(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		t.Run(fmt.Sprintf("oversized=%v", oversized), func(t *testing.T) {
			var responses []*httptest.ResponseRecorder
			for _, generated := range []bool{false, true} {
				h := apiTestHandlers(t)
				router := originalAPIRoutes(h)
				if generated {
					router = h.Routes()
				}
				var body io.Reader = &failingAPIReader{}
				if oversized {
					body = strings.NewReader("title=Changed&ignored=" + strings.Repeat("x", 20<<20))
				}
				counted := &apiCountingReader{Reader: body}
				r := httptest.NewRequest("PATCH", "/1", counted)
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				responses = append(responses, w)
				if oversized {
					assert.LessOrEqual(t, counted.bytesRead, (10<<20)+1, "must stop reading at the existing form size limit")
				}
				link, err := h.database.GetLink(t.Context(), 1)
				require.NoError(t, err)
				assert.Equal(t, "Original title", link.Title)
			}
			require.Equal(t, http.StatusBadRequest, responses[0].Code)
			assert.Equal(t, responses[0].Code, responses[1].Code)
			assert.Equal(t, responses[0].Body.String(), responses[1].Body.String())
		})
	}
}

type apiCountingReader struct {
	io.Reader
	bytesRead int
}

func (r *apiCountingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.bytesRead += n
	return n, err
}
