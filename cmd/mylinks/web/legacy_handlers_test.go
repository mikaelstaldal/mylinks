package web

// These pre-ogen HTTP handlers are a frozen test oracle for wire compatibility.
// They are intentionally excluded from the application.
import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/mikaelstaldal/mylinks/cmd/mylinks/db"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type legacyHandlers struct{ *Handlers }

func (h legacyHandlers) ListLinks(w http.ResponseWriter, r *http.Request) {
	h.listLinks(w, r, http.StatusOK)
}

func (h legacyHandlers) AddItem(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		sendError(w, fmt.Sprintf("Failed to parse form: %v", err), http.StatusBadRequest)
		return
	}

	urlString := r.PostForm.Get("url")
	if urlString != "" {
		// Parse and validate URL
		parsedURL, err := url.Parse(urlString)
		if err != nil || h.validateURL(parsedURL) != nil {
			sendError(w, "Invalid URL. Must be a valid HTTP/HTTPS URL", http.StatusBadRequest)
			return
		}
		h.addLink(w, r, parsedURL)
	} else {
		if err := r.ParseForm(); err != nil {
			sendError(w, fmt.Sprintf("Failed to parse form: %v", err), http.StatusBadRequest)
			return
		}

		title := strings.TrimSpace(r.PostForm.Get("note-title"))
		if title == "" {
			sendError(w, "Note title is required", http.StatusBadRequest)
			return
		}
		if len(title) > maxTitleLength {
			sendError(w, fmt.Sprintf("Note title is too long, max %d characters allowed", maxTitleLength), http.StatusBadRequest)
			return
		}

		note := strings.TrimSpace(r.PostForm.Get("note-text"))
		if note == "" {
			sendError(w, "Note text is required", http.StatusBadRequest)
			return
		}
		if len(note) > maxDescriptionLength {
			sendError(w, fmt.Sprintf("Note text is too long, max %d characters allowed", maxDescriptionLength), http.StatusBadRequest)
			return
		}
		h.addNote(w, r, title, note)
	}
}

func (h legacyHandlers) addLink(w http.ResponseWriter, r *http.Request, urlToSave *url.URL) {
	id, errMsg, status := h.saveLink(r.Context(), urlToSave)
	if errMsg != "" {
		sendError(w, errMsg, status)
		return
	}

	w.Header().Set("Location", fmt.Sprintf("/%v", id))
	h.listLinks(w, r, http.StatusCreated)
}

func (h legacyHandlers) addNote(w http.ResponseWriter, r *http.Request, title string, note string) {
	description := note

	// Generate a pseudo URL to satisfy the NOT NULL UNIQUE constraint and keep entries distinguishable.
	urlToSave := fmt.Sprintf("note:%d", time.Now().UnixMilli())

	writeCtx, cancel := writeContext(r.Context())
	defer cancel()
	id, err := h.database.AddLink(writeCtx, urlToSave, title, description, []byte(note))
	if err != nil {
		sendError(w, fmt.Sprintf("Failed to add note: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Location", fmt.Sprintf("/%v", id))
	h.listLinks(w, r, http.StatusCreated)
}

func (h legacyHandlers) BookmarkletSave(w http.ResponseWriter, r *http.Request) {
	urlString := r.URL.Query().Get("url")
	if urlString == "" {
		h.render(w, "bookmarklet-result.html", struct {
			Success bool
			URL     string
			Error   string
		}{Success: false, Error: "No URL provided"}, http.StatusBadRequest)
		return
	}

	parsedURL, err := url.Parse(urlString)
	if err != nil || h.validateURL(parsedURL) != nil {
		h.render(w, "bookmarklet-result.html", struct {
			Success bool
			URL     string
			Error   string
		}{Success: false, Error: "Invalid URL format. Must be a valid HTTP/HTTPS URL"}, http.StatusBadRequest)
		return
	}

	_, errMsg, status := h.saveLink(r.Context(), parsedURL)
	if errMsg != "" {
		h.render(w, "bookmarklet-result.html", struct {
			Success bool
			URL     string
			Error   string
		}{Success: false, Error: errMsg}, status)
		return
	}

	h.render(w, "bookmarklet-result.html", struct {
		Success bool
		URL     string
		Error   string
	}{Success: true, URL: urlString}, http.StatusCreated)
}

func (h legacyHandlers) GetLink(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		sendError(w, fmt.Sprintf("Invalid ID: %v", err), http.StatusBadRequest)
		return
	}

	h.getLink(w, r, id)
}

func (h legacyHandlers) EditLink(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		sendError(w, fmt.Sprintf("Invalid ID: %v", err), http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		sendError(w, fmt.Sprintf("Failed to parse form: %v", err), http.StatusBadRequest)
		return
	}

	title := r.PostForm.Get("title")
	if title == "" {
		sendError(w, "title is required", http.StatusBadRequest)
		return
	}
	if len(title) > maxTitleLength {
		sendError(w, fmt.Sprintf("title is too long, max %d characters allowed", maxTitleLength), http.StatusBadRequest)
		return
	}

	description := r.PostForm.Get("description")
	if len(description) > maxDescriptionLength {
		sendError(w, fmt.Sprintf("description is too long, max %d characters allowed", maxDescriptionLength), http.StatusBadRequest)
		return
	}

	body := h.refetchBody(r.Context(), id, description)

	writeCtx, cancel := writeContext(r.Context())
	defer cancel()
	err = h.database.UpdateLink(writeCtx, id, title, description, body)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			sendError(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		} else {
			sendError(w, fmt.Sprintf("Failed edit link: %v\n", err), http.StatusInternalServerError)
		}
		return
	}

	h.getLink(w, r, id)
}

func (h legacyHandlers) getLink(w http.ResponseWriter, r *http.Request, id int64) {
	dbLink, err := h.database.GetLink(r.Context(), id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			sendError(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		} else {
			sendError(w, fmt.Sprintf("Failed to get link: %v\n", err), http.StatusInternalServerError)
		}
		return
	}

	if wantJson(r) {
		h.renderJson(w, dbLink, http.StatusOK)
	} else {
		view := LinkView{
			Link: dbLink,
			Edit: r.URL.Query().Get("edit") == "1",
		}
		if h.browserContext != nil {
			h.render(w, "link-with-screenshot", view, http.StatusOK)
		} else {
			h.render(w, "link-without-screenshot", view, http.StatusOK)
		}
	}
}

func (h legacyHandlers) DeleteLink(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		sendError(w, fmt.Sprintf("Invalid ID: %v", err), http.StatusBadRequest)
		return
	}

	writeCtx, cancel := writeContext(r.Context())
	defer cancel()
	err = h.database.DeleteLink(writeCtx, id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			sendError(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		} else {
			sendError(w, fmt.Sprintf("Failed delete link: %v\n", err), http.StatusInternalServerError)
		}
		return
	}

	screenshotPath := filepath.Join(h.screenshotsDir, fmt.Sprintf("%d.png", id))
	if err := os.Remove(screenshotPath); err != nil && !os.IsNotExist(err) {
		log.Printf("Failed to delete screenshot: %v\n", err)
	}
}

func (h legacyHandlers) listLinks(w http.ResponseWriter, r *http.Request, status int) {
	search := r.URL.Query().Get("s")
	var dbLinks []db.Link
	var err error
	if search != "" {
		dbLinks, err = h.database.Search(r.Context(), search)
		if err != nil {
			sendError(w, fmt.Sprintf("Failed to search: %v\n", err), http.StatusInternalServerError)
			return
		}
	} else {
		dbLinks, err = h.database.GetAllLinks(r.Context())
		if err != nil {
			sendError(w, fmt.Sprintf("Failed to get links: %v\n", err), http.StatusInternalServerError)
			return
		}
	}

	if wantJson(r) {
		h.renderJson(w, dbLinks, status)
	} else {
		links := make([]LinkView, 0, len(dbLinks))
		for _, link := range dbLinks {
			links = append(links, LinkView{Link: link})
		}
		data := struct {
			Search          string
			Links           []LinkView
			ShowScreenshots bool
		}{
			Search:          search,
			Links:           links,
			ShowScreenshots: h.browserContext != nil,
		}
		var templateName string
		if r.Header.Get("HX-Request") == "true" {
			templateName = "links"
		} else {
			templateName = "index.html"
		}
		h.render(w, templateName, data, status)
	}
}

func (h legacyHandlers) render(w http.ResponseWriter, name string, data any, status int) {
	buf := new(bytes.Buffer)
	err := h.templates.ExecuteTemplate(buf, name, data)
	if err != nil {
		sendError(w, fmt.Sprintf("Failed to render %s: %v\n", name, err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

func (h legacyHandlers) renderJson(w http.ResponseWriter, data any, status int) {
	jsonData, err := json.Marshal(data)
	if err != nil {
		sendError(w, fmt.Sprintf("Failed to marshal JSON: %v\n", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(jsonData)
}
