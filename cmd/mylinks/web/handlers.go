package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/ledongthuc/pdf"
	"github.com/mikaelstaldal/go-server-common/httputil"
	"github.com/mikaelstaldal/mylinks/cmd/mylinks/db"
	"github.com/mikaelstaldal/mylinks/ui"
	"golang.org/x/net/html"
)

const maxTitleLength = 250
const maxDescriptionLength = 1020
const maxBodyLength = 1000000

// Handlers holds dependencies for the handlers.
type Handlers struct {
	executableDir  string
	database       *db.DB
	screenshotsDir string
	templates      *template.Template
	client         *http.Client
	browserContext context.Context
	forTesting     bool
}

// NewHandlers creates a new Handlers.
func NewHandlers(executableDir string, database *db.DB, screenshotsDir string) *Handlers {
	return newHandlers(executableDir, database, screenshotsDir, false)
}

func newHandlers(executableDir string, database *db.DB, screenshotsDir string, forTesting bool) *Handlers {
	templates := template.New("").Funcs(template.FuncMap{
		"screenshotFilename": screenshotFilename,
		"isNote":             isNote,
	})

	templatesDir := filepath.Join(executableDir, "ui/templates")
	templateFiles, err := filepath.Glob(filepath.Join(templatesDir, "*.html"))
	if err != nil {
		log.Fatalf("Unable to glob templates: %v", err)
	}

	if len(templateFiles) > 0 {
		templates, err = templates.ParseFiles(templateFiles...)
	} else {
		templates, err = templates.ParseFS(ui.Files, "templates/*.html")
	}
	if err != nil {
		log.Fatalf("Unable to read templates: %v", err)
	}
	if templates == nil {
		log.Fatalf("No templates found")
	}

	// Create an HTTP client with improved configuration to handle various websites,
	// hardened against SSRF: the dialer validates the resolved IP at connect time
	// (closing the TOCTOU / DNS-rebinding window between validation and fetch) and
	// CheckRedirect re-validates every redirect target. In tests, loopback addresses
	// (the mock httptest servers) must remain reachable, so the safe dialer and
	// redirect validation are skipped when forTesting is set.
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	dialContext := dialer.DialContext
	if !forTesting {
		dialContext = httputil.SafeDialContext(dialer)
	}
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: dialContext,
			// Force HTTP/1.1 to avoid HTTP/2 issues with some websites
			ForceAttemptHTTP2: false,
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: false,
			},
			// Set reasonable timeouts
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 5 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			if forTesting {
				return nil
			}
			return httputil.ValidateExternalURL(req.URL.String())
		},
	}

	var browserContext context.Context
	dockerURL := os.Getenv("CHROMEDP")
	if dockerURL != "" {
		if err := os.MkdirAll(screenshotsDir, 0700); err != nil {
			log.Fatalf("failed to create screenshots directory: %v", err)
		}

		allocatorContext, _ := chromedp.NewRemoteAllocator(context.Background(), dockerURL)
		browserContext, _ = chromedp.NewContext(allocatorContext)
	}

	return &Handlers{
		executableDir:  executableDir,
		database:       database,
		screenshotsDir: screenshotsDir,
		templates:      templates,
		client:         client,
		browserContext: browserContext,
		forTesting:     forTesting,
	}
}

// Routes sets up and returns the HTTP routing handler for the application.
// It configures routes for static files, screenshots (if enabled), and API endpoints.
// Static files are served either from the local filesystem or embedded files.
// If authentication is configured (via usernameBcryptHash and passwordBcryptHash),
// all routes will be protected with HTTP Basic Authentication.
// Returns an http.Handler that includes all configured routes with common headers.
func (h *Handlers) Routes() http.Handler {
	mux := http.NewServeMux()

	staticDir := filepath.Join(h.executableDir, "ui/static")
	staticFiles, err := filepath.Glob(filepath.Join(staticDir, "*"))
	if err != nil {
		log.Fatalf("Unable to glob static files: %v", err)
	}
	if len(staticFiles) > 0 {
		mux.Handle("GET /static/", http.StripPrefix("/static", http.FileServer(http.Dir(staticDir))))
	} else {
		mux.Handle("GET /static/", http.FileServerFS(ui.Files))
	}

	if h.browserContext != nil {
		mux.Handle("GET /screenshots/", http.StripPrefix("/screenshots", http.FileServer(http.Dir(h.screenshotsDir))))
	}

	mux.HandleFunc("GET /bookmarklet", h.BookmarkletSave)

	mux.HandleFunc("GET /{$}", h.ListLinks)
	mux.HandleFunc("POST /{$}", h.AddItem)
	mux.HandleFunc("GET /{id}", h.GetLink)
	mux.HandleFunc("PATCH /{id}", h.EditLink)
	mux.HandleFunc("DELETE /{id}", h.DeleteLink)

	return commonHeaders(mux)
}

type Link struct {
	ID          int64
	URL         string
	Title       string
	Description string
	AddedAt     time.Time
	Screenshot  string
}

type LinkView struct {
	db.Link
	Edit bool
}

// ListLinks handles the request to list all links.
func (h *Handlers) ListLinks(w http.ResponseWriter, r *http.Request) {
	h.listLinks(w, r, http.StatusOK)
}

// AddItem handles the request to add a new item.
func (h *Handlers) AddItem(w http.ResponseWriter, r *http.Request) {
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

// saveLink fetches the URL, extracts metadata, and saves it to the database.
// Returns the link ID, an error message, and an HTTP status code.
func (h *Handlers) saveLink(urlToSave *url.URL) (int64, string, int) {
	var title, description string
	var body []byte
	var screenshot []byte
	var err error
	if h.browserContext != nil {
		title, description, body, screenshot, err = h.extractTitleAndDescriptionAndBodyAndScreenshotFromURL(urlToSave)
		if err != nil {
			return 0, fmt.Sprintf("Failed to load URL: %v", err), http.StatusBadRequest
		}
	} else {
		title, description, body, err = h.extractTitleAndDescriptionAndBodyFromURL(urlToSave)
		if err != nil {
			return 0, fmt.Sprintf("Failed to load URL: %v", err), http.StatusBadRequest
		}
	}

	id, err := h.database.AddLink(urlToSave.String(), title, description, body)
	if err != nil {
		if errors.Is(err, db.ErrDuplicate) {
			return 0, "URL already exists", http.StatusConflict
		}
		return 0, fmt.Sprintf("Failed to add link: %v", err), http.StatusInternalServerError
	}

	if screenshot != nil {
		if err = h.saveScreenshot(urlToSave.String(), screenshot); err != nil {
			return 0, fmt.Sprintf("Failed to save screenshot: %v", err), http.StatusInternalServerError
		}
	}

	return id, "", http.StatusCreated
}

// addLink handles the request to add a new link.
func (h *Handlers) addLink(w http.ResponseWriter, r *http.Request, urlToSave *url.URL) {
	id, errMsg, status := h.saveLink(urlToSave)
	if errMsg != "" {
		sendError(w, errMsg, status)
		return
	}

	w.Header().Set("Location", fmt.Sprintf("/%v", id))
	h.listLinks(w, r, http.StatusCreated)
}

// addNote handles the request to add a new free-form note.
func (h *Handlers) addNote(w http.ResponseWriter, r *http.Request, title string, note string) {
	description := note

	// Generate a pseudo URL to satisfy the NOT NULL UNIQUE constraint and keep entries distinguishable.
	urlToSave := fmt.Sprintf("note:%d", time.Now().UnixMilli())

	id, err := h.database.AddLink(urlToSave, title, description, []byte(note))
	if err != nil {
		sendError(w, fmt.Sprintf("Failed to add note: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Location", fmt.Sprintf("/%v", id))
	h.listLinks(w, r, http.StatusCreated)
}

// BookmarkletSave handles GET /bookmarklet?url=... to save a link from the bookmarklet popup.
func (h *Handlers) BookmarkletSave(w http.ResponseWriter, r *http.Request) {
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

	_, errMsg, status := h.saveLink(parsedURL)
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

func (h *Handlers) validateURL(u *url.URL) error {
	if h.forTesting {
		return nil
	}

	return httputil.ValidateExternalURL(u.String())
}

// extractTitleAndDescriptionAndBodyFromURL fetches the URL and extracts the page title from HTML.
func (h *Handlers) extractTitleAndDescriptionAndBodyFromURL(url *url.URL) (string, string, []byte, error) {
	req, err := http.NewRequest("GET", url.String(), nil)
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to create request: %w", err)
	}

	// AddItem browser-like headers to avoid being blocked by anti-bot measures
	req.Header.Set("User-Agent", "Mylinks/1.0")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Upgrade-Insecure-Requests", "1")

	resp, err := h.client.Do(req)
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to fetch URL: %w", err)
	}
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyLength))
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to fetch URL: %w", err)
	}

	contentType := resp.Header.Get("Content-Type")
	if resp.StatusCode != http.StatusOK {
		// Degrade gracefully for bot-detection pages (e.g. Cloudflare): a 403 with an HTML body
		// indicates a browser challenge rather than a real error. For all other non-200 responses
		// (e.g. 404), return an error.
		if resp.StatusCode == http.StatusForbidden &&
			(strings.HasPrefix(strings.ToLower(contentType), "text/html") ||
				strings.HasPrefix(strings.ToLower(contentType), "application/xhtml+xml")) {
			log.Printf("HTTP 403 with HTML body fetching %s (bot detection?), saving with unknown title", url)
			return "(unknown)", "", nil, nil
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			log.Printf("HTTP 429 fetching %s (bot detection?), saving with unknown title", url)
			return "(unknown)", "", nil, nil
		}
		return "", "", nil, fmt.Errorf("HTTP error: %d", resp.StatusCode)
	}
	if strings.HasPrefix(strings.ToLower(contentType), "text/html") || strings.HasPrefix(strings.ToLower(contentType), "application/xhtml+xml") {
		return h.extractTitleAndDescriptionAndBodyFromHtml(responseBody)
	} else if strings.ToLower(contentType) == "application/pdf" {
		return h.extractTitleAndDescriptionAndBodyFromPdf(url, responseBody)
	} else {
		return h.extractTitleFromURL(url), contentType, nil, nil
	}
}

func (h *Handlers) extractTitleAndDescriptionAndBodyFromHtml(responseBody []byte) (string, string, []byte, error) {
	doc, err := html.Parse(bytes.NewReader(responseBody))
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to parse HTML: %w", err)
	}

	title := strings.TrimSpace(extractTitleFromHtml(doc))
	if title == "" {
		return "", "", nil, fmt.Errorf("no title found in HTML")
	}

	description := strings.TrimSpace(extractDescriptionFromHtml(doc))

	if len(title) > maxTitleLength {
		title = title[:maxTitleLength] + "..."
	}

	if len(description) > maxDescriptionLength {
		description = description[:maxDescriptionLength] + "..."
	}

	bodyIndex := bytes.Index(responseBody, []byte("<body>"))
	if bodyIndex > 0 {
		responseBody = responseBody[bodyIndex:]
	}

	return title, description, responseBody, nil
}

// extractTitleFromHtml recursively searches for the "title" element in the HTML tree.
func extractTitleFromHtml(n *html.Node) string {
	if n.Type == html.ElementNode && n.Data == "title" {
		// Found the title element, extract its text content
		return extractTextContent(n)
	}

	// Recursively search child nodes
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if title := extractTitleFromHtml(c); title != "" {
			return title
		}
	}

	return ""
}

// extractTextContent extracts all text content from a node and its children.
func extractTextContent(n *html.Node) string {
	var text strings.Builder

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		switch c.Type {
		case html.TextNode:
			text.WriteString(c.Data)
		case html.ElementNode:
			text.WriteString(extractTextContent(c))
		}
	}

	return text.String()
}

// extractDescriptionFromHtml recursively searches for the "meta" element in the HTML tree.
func extractDescriptionFromHtml(n *html.Node) string {
	if n.Type == html.ElementNode && n.Data == "meta" && extractAttribute(n, "name") == "description" {
		return extractAttribute(n, "content")
	}

	// Recursively search child nodes
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if title := extractDescriptionFromHtml(c); title != "" {
			return title
		}
	}

	return ""
}

func extractAttribute(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func (h *Handlers) extractTitleAndDescriptionAndBodyFromPdf(url *url.URL, responseBody []byte) (string, string, []byte, error) {
	r, err := pdf.NewReader(bytes.NewReader(responseBody), int64(len(responseBody)))
	if err != nil {
		return h.extractTitleFromURL(url), "PDF", nil, nil
	}

	b, err := r.GetPlainText()
	if err != nil {
		return h.extractTitleFromURL(url), "PDF", nil, nil
	}
	pdfText, err := io.ReadAll(io.LimitReader(b, maxBodyLength))
	_, _ = io.Copy(io.Discard, b)
	if err != nil {
		return h.extractTitleFromURL(url), "PDF", nil, nil
	}

	var title string
	var description string
	totalPage := r.NumPage()
pages:
	for pageIndex := 1; pageIndex <= totalPage; pageIndex++ {
		p := r.Page(pageIndex)
		if p.V.IsNull() || p.V.Key("Contents").Kind() == pdf.Null {
			continue
		}

		rows, _ := p.GetTextByRow()
		for _, row := range rows {
			var sb strings.Builder
			for _, word := range row.Content {
				sb.WriteString(word.S)
			}
			rowText := strings.TrimSpace(sb.String())
			if rowText == "" {
				continue
			}
			if title == "" {
				title = rowText
			} else if description == "" {
				description = rowText
			} else {
				break pages
			}
		}
	}

	return title, description, pdfText, nil
}

func (h *Handlers) extractTitleFromURL(url *url.URL) string {
	path := strings.TrimRight(url.Path, "/")
	lastSegment := filepath.Base(path)
	var title string
	if lastSegment != "" && lastSegment != "." && lastSegment != "/" {
		title = lastSegment
	} else {
		title = url.Host
	}
	return title
}

func (h *Handlers) extractTitleAndDescriptionAndBodyAndScreenshotFromURL(url *url.URL) (string, string, []byte, []byte, error) {
	response, err := chromedp.RunResponse(h.browserContext,
		chromedp.Navigate(url.String()),
	)
	if err != nil {
		return "", "", nil, nil, fmt.Errorf("failed to fetch URL: %w", err)
	}
	if response.Status >= 400 {
		// Degrade gracefully for Cloudflare-style bot detection (403). For all other errors, fail.
		if response.Status == http.StatusForbidden {
			log.Printf("HTTP 403 fetching %s (bot detection?), saving with unknown title", url)
			return "(unknown)", "", nil, nil, nil
		}
		return "", "", nil, nil, fmt.Errorf("failed to fetch URL: %v %v", response.Status, response.StatusText)
	}

	var title string
	err = chromedp.Run(h.browserContext,
		chromedp.Title(&title),
	)
	if err != nil {
		return "", "", nil, nil, fmt.Errorf("failed to extract title: %w", err)
	}
	title = strings.TrimSpace(title)

	var description string
	err = chromedp.Run(h.browserContext,
		chromedp.Evaluate(`document.querySelector("head meta[name='description']").content`, &description),
	)
	if err != nil {
		description = ""
	}
	description = strings.TrimSpace(description)

	var body []byte
	err = chromedp.Run(h.browserContext,
		chromedp.JavascriptAttribute(`body`, "outerHTML", &body),
	)
	if err != nil {
		log.Printf("failed to extract body: %v", err)
	}

	var screenshot []byte
	err = chromedp.Run(h.browserContext,
		chromedp.EmulateViewport(800, 600),
		chromedp.ActionFunc(func(ctx context.Context) error {
			var err error
			screenshot, err = page.CaptureScreenshot().
				WithFromSurface(true).
				WithFormat(page.CaptureScreenshotFormatPng).
				WithQuality(100).
				Do(ctx)
			if err != nil {
				return err
			}
			return nil
		}),
	)
	if err != nil {
		return "", "", nil, nil, fmt.Errorf("failed to take screenshot: %w", err)
	}

	if title == "" {
		return "", "", nil, nil, fmt.Errorf("no title found in HTML")
	}

	if len(title) > maxTitleLength {
		title = title[:maxTitleLength] + "..." + "..."
	}

	if len(description) > maxDescriptionLength {
		description = description[:maxDescriptionLength] + "..."
	}

	if len(body) > maxBodyLength {
		body = body[:maxBodyLength]
	}

	return title, description, body, screenshot, nil
}

func (h *Handlers) saveScreenshot(urlString string, screenshot []byte) error {
	filename := screenshotFilename(urlString)
	path := filepath.Join(h.screenshotsDir, filename)

	if err := os.WriteFile(path, screenshot, 0644); err != nil {
		return fmt.Errorf("failed to write screenshot file: %w", err)
	}

	return nil
}

// GetLink gets a single link.
func (h *Handlers) GetLink(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		sendError(w, fmt.Sprintf("Invalid ID: %v", err), http.StatusBadRequest)
		return
	}

	h.getLink(w, r, id)
}

// EditLink handles the request to edit a link.
func (h *Handlers) EditLink(w http.ResponseWriter, r *http.Request) {
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

	err = h.database.UpdateLink(id, title, description)
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

func (h *Handlers) getLink(w http.ResponseWriter, r *http.Request, id int64) {
	dbLink, err := h.database.GetLink(id)
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

// DeleteLink handles the request to delete a link.
func (h *Handlers) DeleteLink(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		sendError(w, fmt.Sprintf("Invalid ID: %v", err), http.StatusBadRequest)
		return
	}

	err = h.database.DeleteLink(id)
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

func (h *Handlers) listLinks(w http.ResponseWriter, r *http.Request, status int) {
	search := r.URL.Query().Get("s")
	var dbLinks []db.Link
	var err error
	if search != "" {
		dbLinks, err = h.database.Search(search)
		if err != nil {
			sendError(w, fmt.Sprintf("Failed to search: %v\n", err), http.StatusInternalServerError)
			return
		}
	} else {
		dbLinks, err = h.database.GetAllLinks()
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

func wantJson(r *http.Request) bool {
	wantJson := false
	for _, accept := range r.Header.Values("Accept") {
		if strings.ToLower(accept) == "application/json" {
			wantJson = true
		}
	}
	return wantJson
}

func (h *Handlers) render(w http.ResponseWriter, name string, data any, status int) {
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

func (h *Handlers) renderJson(w http.ResponseWriter, data any, status int) {
	jsonData, err := json.Marshal(data)
	if err != nil {
		sendError(w, fmt.Sprintf("Failed to marshal JSON: %v\n", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(jsonData)
}

func sendError(w http.ResponseWriter, errorMessage string, status int) {
	var message string
	if status >= 500 {
		log.Println(errorMessage + "\n" + string(debug.Stack()))
		message = http.StatusText(status)
	} else {
		message = errorMessage
	}
	w.WriteHeader(status)
	_, _ = fmt.Fprintln(w, message)
}

func screenshotFilename(urlString string) string {
	hash := sha256.Sum256([]byte(urlString))
	return hex.EncodeToString(hash[:]) + ".png"
}

func isNote(urlString string) bool {
	return strings.HasPrefix(urlString, "note:")
}
