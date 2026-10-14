package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
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
	"github.com/mikaelstaldal/mylinks/internal/api"
	"github.com/mikaelstaldal/mylinks/ui"
	"golang.org/x/net/html"
)

const maxTitleLength = 250
const maxDescriptionLength = 1020
const maxBodyLength = 1000000

// databaseWriteTimeout bounds how long a database write may take, including
// waiting for a free connection from the pool.
const databaseWriteTimeout = 10 * time.Second

// writeContext returns the context to use for a database write. Reads are
// abandoned when the client goes away, but writes are not: the user asked for
// the change, and for a link that also means throwing away an already
// completed fetch of the page. The timeout still bounds the wait.
func writeContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), databaseWriteTimeout)
}

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
// Authentication is applied by main when configured.
// Returns an http.Handler that includes all configured routes with common headers.
func (h *Handlers) Routes() http.Handler {
	mux := http.NewServeMux()
	apiServer, err := api.NewServer(h, h, api.WithErrorHandler(handleAPIError))
	if err != nil {
		log.Fatalf("Unable to initialize API server: %v", err)
	}

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

	serveAPI := prepareAPIRequest(apiServer)
	mux.HandleFunc("GET /bookmarklet", serveAPI)
	mux.HandleFunc("GET /{$}", serveAPI)
	mux.HandleFunc("POST /{$}", serveAPI)
	mux.HandleFunc("GET /{id}", serveAPI)
	mux.HandleFunc("PATCH /{id}", serveAPI)
	mux.HandleFunc("DELETE /{id}", serveAPI)

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

// saveLink fetches the URL, extracts metadata, and saves it to the database.
// Returns the link ID, an error message, and an HTTP status code.
func (h *Handlers) saveLink(ctx context.Context, urlToSave *url.URL) (int64, string, int) {
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
		title, description, body, err = h.extractTitleAndDescriptionAndBodyFromURL(ctx, urlToSave)
		if err != nil {
			return 0, fmt.Sprintf("Failed to load URL: %v", err), http.StatusBadRequest
		}
	}

	writeCtx, cancel := writeContext(ctx)
	defer cancel()
	id, err := h.database.AddLink(writeCtx, urlToSave.String(), title, description, body)
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

// refetchBody returns a body to store for a link which has none, or nil to keep
// what is stored. Links added before bodies were stored separately from the
// contentless FTS index have no body to carry through an edit, so the page is
// fetched again to keep it searchable.
//
// This is best effort: the user asked to edit the link, not to fetch it, so a
// failure here is logged and the edit proceeds without a body.
func (h *Handlers) refetchBody(ctx context.Context, id int64, description string) []byte {
	link, err := h.database.GetLink(ctx, id)
	if err != nil {
		// A missing link is reported by the update itself.
		if !errors.Is(err, db.ErrNotFound) {
			log.Printf("Unable to look up link %d to refetch its body: %v", id, err)
		}
		return nil
	}

	if isNote(link.URL) {
		// A note has no page to fetch, its text is its body. It is rewritten on
		// every edit, unlike a page body: an edited note must not keep matching
		// the text it no longer holds. make gives a non-nil slice for an empty
		// note, which stores it as an empty body rather than keeping the old one.
		body := make([]byte, len(description))
		copy(body, description)
		return body
	}

	hasBody, err := h.database.HasBody(ctx, id)
	if err != nil {
		if !errors.Is(err, db.ErrNotFound) {
			log.Printf("Unable to check whether link %d has a body: %v", id, err)
		}
		return nil
	}
	if hasBody {
		return nil
	}

	parsedURL, err := url.Parse(link.URL)
	if err != nil || h.validateURL(parsedURL) != nil {
		log.Printf("Not refetching the body of link %d, %s is no longer a valid URL", id, link.URL)
		return nil
	}

	var body []byte
	if h.browserContext != nil {
		// The screenshot is a by-product here, the one taken when the link was
		// added is left alone.
		_, _, body, _, err = h.extractTitleAndDescriptionAndBodyAndScreenshotFromURL(parsedURL)
	} else {
		_, _, body, err = h.extractTitleAndDescriptionAndBodyFromURL(ctx, parsedURL)
	}
	if err != nil {
		log.Printf("Unable to refetch %s to index its body: %v", link.URL, err)
		return nil
	}

	return body
}

func (h *Handlers) validateURL(u *url.URL) error {
	if h.forTesting {
		return nil
	}

	return httputil.ValidateExternalURL(u.String())
}

// extractTitleAndDescriptionAndBodyFromURL fetches the URL and extracts the page title from HTML.
func (h *Handlers) extractTitleAndDescriptionAndBodyFromURL(ctx context.Context, url *url.URL) (string, string, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url.String(), nil)
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

// extractTitleAndDescriptionAndBodyAndScreenshotFromURL does not take a
// context: the browser context is shared between requests, so aborting a
// navigation halfway would leave it in an undefined state for other requests.
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

func wantJson(r *http.Request) bool {
	wantJson := false
	for _, accept := range r.Header.Values("Accept") {
		if strings.ToLower(accept) == "application/json" {
			wantJson = true
		}
	}
	return wantJson
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

var _ api.Handler = (*Handlers)(nil)
var _ api.SecurityHandler = (*Handlers)(nil)

// ListLinks implements the generated list operation.
func (h *Handlers) ListLinks(ctx context.Context, params api.ListLinksParams) (api.ListLinksRes, error) {
	links, err := h.listLinks(ctx, params.S.Or(""))
	if err != nil {
		return nil, err
	}
	if wantsJSON(ctx) {
		response, err := linkResponses(links)
		if err != nil {
			return nil, err
		}
		return &response, nil
	}
	body, err := h.listHTML(links, params.S.Or(""), params.HXRequest.Or("") == "true")
	if err != nil {
		return nil, err
	}
	return &api.ListTextHTML{Data: body}, nil
}

// AddItem implements the generated form operation.
func (h *Handlers) AddItem(ctx context.Context, req api.OptAddItemReq, params api.AddItemParams) (api.AddItemRes, error) {
	form := req.Value
	var id int64
	if urlString := form.URL.Or(""); urlString != "" {
		parsedURL, err := url.Parse(urlString)
		if err != nil || h.validateURL(parsedURL) != nil {
			return nil, requestError(http.StatusBadRequest, "Invalid URL. Must be a valid HTTP/HTTPS URL")
		}
		var message string
		var status int
		id, message, status = h.saveLink(ctx, parsedURL)
		if message != "" {
			return nil, requestError(status, message)
		}
	} else {
		title := strings.TrimSpace(form.NoteMinusTitle.Or(""))
		if title == "" {
			return nil, requestError(http.StatusBadRequest, "Note title is required")
		}
		if len(title) > maxTitleLength {
			return nil, requestError(http.StatusBadRequest, fmt.Sprintf("Note title is too long, max %d characters allowed", maxTitleLength))
		}
		note := strings.TrimSpace(form.NoteMinusText.Or(""))
		if note == "" {
			return nil, requestError(http.StatusBadRequest, "Note text is required")
		}
		if len(note) > maxDescriptionLength {
			return nil, requestError(http.StatusBadRequest, fmt.Sprintf("Note text is too long, max %d characters allowed", maxDescriptionLength))
		}
		writeCtx, cancel := writeContext(ctx)
		defer cancel()
		var err error
		id, err = h.database.AddLink(writeCtx, fmt.Sprintf("note:%d", time.Now().UnixMilli()), title, note, []byte(note))
		if err != nil {
			return nil, requestError(http.StatusInternalServerError, fmt.Sprintf("Failed to add note: %v", err))
		}
	}
	location := fmt.Sprintf("/%d", id)
	links, err := h.listLinks(ctx, params.S.Or(""))
	if err != nil {
		return nil, errorWithLocation(err, location)
	}
	if wantsJSON(ctx) {
		response, err := linkResponses(links)
		if err != nil {
			return nil, errorWithLocation(err, location)
		}
		return &api.LinksHeaders{Location: api.NewOptString(location), Response: response}, nil
	}
	body, err := h.listHTML(links, params.S.Or(""), params.HXRequest.Or("") == "true")
	if err != nil {
		return nil, errorWithLocation(err, location)
	}
	return &api.AddItemCreatedTextHTMLHeaders{
		Location: api.NewOptString(location), Response: api.AddItemCreatedTextHTML{Data: body},
	}, nil
}

// BookmarkletSave always returns the generated HTML result response.
func (h *Handlers) BookmarkletSave(ctx context.Context, params api.BookmarkletSaveParams) (api.BookmarkletSaveRes, error) {
	urlString := params.URL.Or("")
	message := ""
	var status int
	if urlString == "" {
		message, status = "No URL provided", http.StatusBadRequest
	} else {
		parsedURL, err := url.Parse(urlString)
		if err != nil || h.validateURL(parsedURL) != nil {
			message, status = "Invalid URL format. Must be a valid HTTP/HTTPS URL", http.StatusBadRequest
		} else {
			_, message, status = h.saveLink(ctx, parsedURL)
		}
	}
	displayedURL := ""
	if message == "" {
		displayedURL = urlString
	}
	body, err := h.renderTemplate("bookmarklet-result.html", struct {
		Success bool
		URL     string
		Error   string
	}{Success: message == "", URL: displayedURL, Error: message})
	if err != nil {
		return nil, err
	}
	switch status {
	case http.StatusCreated:
		return &api.BookmarkletSaveCreated{Data: body}, nil
	case http.StatusBadRequest:
		return &api.BookmarkletSaveBadRequest{Data: body}, nil
	case http.StatusConflict:
		return &api.BookmarkletSaveConflict{Data: body}, nil
	default:
		return &api.BookmarkletSaveInternalServerError{Data: body}, nil
	}
}

func (h *Handlers) GetLink(ctx context.Context, params api.GetLinkParams) (api.GetLinkRes, error) {
	return h.getLink(ctx, params.ID, params.Edit.Or("") == "1")
}

func (h *Handlers) EditLink(ctx context.Context, req api.OptEditLinkReq, params api.EditLinkParams) (api.EditLinkRes, error) {
	id, err := parseID(params.ID)
	if err != nil {
		return nil, err
	}
	title := req.Value.Title.Or("")
	if title == "" {
		return nil, requestError(http.StatusBadRequest, "title is required")
	}
	if len(title) > maxTitleLength {
		return nil, requestError(http.StatusBadRequest, fmt.Sprintf("title is too long, max %d characters allowed", maxTitleLength))
	}
	description := req.Value.Description.Or("")
	if len(description) > maxDescriptionLength {
		return nil, requestError(http.StatusBadRequest, fmt.Sprintf("description is too long, max %d characters allowed", maxDescriptionLength))
	}
	body := h.refetchBody(ctx, id, description)
	writeCtx, cancel := writeContext(ctx)
	defer cancel()
	if err := h.database.UpdateLink(writeCtx, id, title, description, body); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, requestError(http.StatusNotFound, http.StatusText(http.StatusNotFound))
		}
		return nil, requestError(http.StatusInternalServerError, fmt.Sprintf("Failed edit link: %v\n", err))
	}
	return h.getLink(ctx, params.ID, params.Edit.Or("") == "1")
}

func (h *Handlers) DeleteLink(ctx context.Context, params api.DeleteLinkParams) (api.DeleteLinkRes, error) {
	id, err := parseID(params.ID)
	if err != nil {
		return nil, err
	}
	writeCtx, cancel := writeContext(ctx)
	defer cancel()
	if err := h.database.DeleteLink(writeCtx, id); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, requestError(http.StatusNotFound, http.StatusText(http.StatusNotFound))
		}
		return nil, requestError(http.StatusInternalServerError, fmt.Sprintf("Failed delete link: %v\n", err))
	}
	screenshotPath := filepath.Join(h.screenshotsDir, fmt.Sprintf("%d.png", id))
	if err := os.Remove(screenshotPath); err != nil && !os.IsNotExist(err) {
		log.Printf("Failed to delete screenshot: %v\n", err)
	}
	return &api.DeleteLinkOK{}, nil
}

// Both get and edit return the same generated item representations.
type itemResponse interface {
	api.GetLinkRes
	api.EditLinkRes
}

func (h *Handlers) getLink(ctx context.Context, idString string, edit bool) (itemResponse, error) {
	id, err := parseID(idString)
	if err != nil {
		return nil, err
	}
	link, err := h.database.GetLink(ctx, id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, requestError(http.StatusNotFound, http.StatusText(http.StatusNotFound))
		}
		return nil, requestError(http.StatusInternalServerError, fmt.Sprintf("Failed to get link: %v\n", err))
	}
	if wantsJSON(ctx) {
		response, err := linkResponse(link)
		if err != nil {
			return nil, err
		}
		return &response, nil
	}
	templateName := "link-without-screenshot"
	if h.browserContext != nil {
		templateName = "link-with-screenshot"
	}
	body, err := h.renderTemplate(templateName, LinkView{Link: link, Edit: edit})
	if err != nil {
		return nil, err
	}
	return &api.ItemTextHTML{Data: body}, nil
}

func (h *Handlers) listLinks(ctx context.Context, search string) ([]db.Link, error) {
	if search != "" {
		links, err := h.database.Search(ctx, search)
		if err != nil {
			return nil, requestError(http.StatusInternalServerError, fmt.Sprintf("Failed to search: %v\n", err))
		}
		return links, nil
	}
	links, err := h.database.GetAllLinks(ctx)
	if err != nil {
		return nil, requestError(http.StatusInternalServerError, fmt.Sprintf("Failed to get links: %v\n", err))
	}
	return links, nil
}

func (h *Handlers) listHTML(dbLinks []db.Link, search string, fragment bool) (io.Reader, error) {
	links := make([]LinkView, 0, len(dbLinks))
	for _, link := range dbLinks {
		links = append(links, LinkView{Link: link})
	}
	data := struct {
		Search          string
		Links           []LinkView
		ShowScreenshots bool
	}{Search: search, Links: links, ShowScreenshots: h.browserContext != nil}
	name := "index.html"
	if fragment {
		name = "links"
	}
	return h.renderTemplate(name, data)
}

func (h *Handlers) renderTemplate(name string, data any) (io.Reader, error) {
	buf := new(bytes.Buffer)
	if err := h.templates.ExecuteTemplate(buf, name, data); err != nil {
		return nil, requestError(http.StatusInternalServerError, fmt.Sprintf("Failed to render %s: %v\n", name, err))
	}
	return buf, nil
}

// The generated external-type encoder does not propagate json.Marshal errors.
// Only AddedAt can fail to marshal in this model; validate it before returning a
// response so malformed stored timestamps still produce the established 500.
func linkResponse(link db.Link) (api.Link, error) {
	if _, err := link.AddedAt.MarshalJSON(); err != nil {
		return api.Link{}, requestError(http.StatusInternalServerError, fmt.Sprintf("Failed to marshal JSON: %v", err))
	}
	return api.Link(link), nil
}

func linkResponses(links []db.Link) (api.Links, error) {
	var response api.Links
	if links != nil {
		response = make(api.Links, len(links))
	}
	for i, link := range links {
		value, err := linkResponse(link)
		if err != nil {
			return nil, err
		}
		response[i] = value
	}
	return response, nil
}

type handlerError struct {
	status   int
	message  string
	location string
}

func (e *handlerError) Error() string { return e.message }

func requestError(status int, message string) error {
	return &handlerError{status: status, message: message}
}

func errorWithLocation(err error, location string) error {
	var responseError *handlerError
	if errors.As(err, &responseError) {
		responseError.location = location
	}
	return err
}

func parseID(value string) (int64, error) {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, requestError(http.StatusBadRequest, fmt.Sprintf("Invalid ID: %v", err))
	}
	return id, nil
}

func handleAPIError(_ context.Context, w http.ResponseWriter, _ *http.Request, err error) {
	var responseError *handlerError
	if errors.As(err, &responseError) {
		if responseError.location != "" {
			w.Header().Set("Location", responseError.location)
		}
		sendError(w, responseError.message, responseError.status)
		return
	}
	sendError(w, fmt.Sprintf("Failed to serve API: %v", err), http.StatusInternalServerError)
}

// Authentication is enforced by main's optional Basic-auth middleware.
func (h *Handlers) HandleBasicAuth(ctx context.Context, _ api.OperationName, _ api.BasicAuth) (context.Context, error) {
	return ctx, nil
}
