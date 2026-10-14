package web

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/mikaelstaldal/mylinks/internal/api"
)

// The generated server owns API dispatch. The adapter retains the existing
// handlers' wire format: ogen's Accept negotiation, form validation and error
// encoding differ from this application's established browser API.
// Authentication remains in main's optional Basic-auth middleware.
type apiAdapter struct{}

type apiRequestKey struct{}

type apiRequest struct {
	writer  http.ResponseWriter
	request *http.Request
	handler http.HandlerFunc
	read    bytes.Buffer
	body    io.ReadCloser
	readErr error
}

var errAPIResponseWritten = errors.New("API response already written")

// Read records decoder reads, including a terminal I/O failure, for the original
// handler. ogen's form parser has no size limit, unlike net/http.ParseForm;
// apiRoute limits its reader to the latter's 10 MiB limit plus one sentinel byte.
func (a *apiRequest) Read(p []byte) (int, error) {
	n, err := a.body.Read(p)
	_, _ = a.read.Write(p[:n])
	if err != nil && err != io.EOF {
		a.readErr = err
	}
	return n, err
}

type apiReadError struct{ err error }

func (r *apiReadError) Read([]byte) (int, error) {
	if r.err != nil {
		err := r.err
		r.err = nil
		return 0, err
	}
	return 0, io.EOF
}

func (a *apiRequest) serve() {
	// ogen decodes a cloned request. Replay anything it read so ParseForm in
	// the original handler sees the original body, including malformed forms.
	if a.body != nil {
		a.request.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(&a.read, &apiReadError{err: a.readErr}, a.body), a.body}
	}
	a.handler(a.writer, a.request)
}

func serveAPI(ctx context.Context) error {
	ctx.Value(apiRequestKey{}).(*apiRequest).serve()
	return errAPIResponseWritten
}

func newAPIServer() (*api.Server, error) {
	adapter := apiAdapter{}
	fallback := func(w http.ResponseWriter, r *http.Request) {
		r.Context().Value(apiRequestKey{}).(*apiRequest).serve()
	}
	return api.NewServer(adapter, adapter,
		api.WithErrorHandler(func(ctx context.Context, w http.ResponseWriter, r *http.Request, err error) {
			if !errors.Is(err, errAPIResponseWritten) {
				// Decode failures must retain the original handler's validation,
				// status and message (rather than introduce 415 or JSON errors).
				fallback(w, r)
			}
		}),
		api.WithNotFound(fallback),
		api.WithMethodNotAllowed(func(w http.ResponseWriter, r *http.Request, _ string) {
			fallback(w, r)
		}),
	)
}

// apiRoute keeps ServeMux's existing path matching, HEAD support and 405/Allow
// behavior, while sending every matched API request through the generated server.
func apiRoute(server *api.Server, handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		state := &apiRequest{writer: w, request: r, handler: handler, body: r.Body}
		generatedRequest := r.Clone(context.WithValue(r.Context(), apiRequestKey{}, state))
		if r.Body != nil {
			generatedRequest.Body = struct {
				io.Reader
				io.Closer
			}{io.LimitReader(state, 10<<20+1), r.Body}
		}
		server.ServeHTTP(w, generatedRequest)
	}
}

func (apiAdapter) HandleBasicAuth(ctx context.Context, _ api.OperationName, _ api.BasicAuth) (context.Context, error) {
	return ctx, nil
}

func (apiAdapter) ListLinks(ctx context.Context, _ api.ListLinksParams) (api.ListLinksRes, error) {
	return nil, serveAPI(ctx)
}

func (apiAdapter) AddItem(ctx context.Context, _ api.OptAddItemReq, _ api.AddItemParams) (api.AddItemRes, error) {
	return nil, serveAPI(ctx)
}

func (apiAdapter) GetLink(ctx context.Context, _ api.GetLinkParams) (api.GetLinkRes, error) {
	return nil, serveAPI(ctx)
}

func (apiAdapter) EditLink(ctx context.Context, _ api.OptEditLinkReq, _ api.EditLinkParams) (api.EditLinkRes, error) {
	return nil, serveAPI(ctx)
}

func (apiAdapter) DeleteLink(ctx context.Context, _ api.DeleteLinkParams) (api.DeleteLinkRes, error) {
	return nil, serveAPI(ctx)
}

func (apiAdapter) BookmarkletSave(ctx context.Context, _ api.BookmarkletSaveParams) (api.BookmarkletSaveRes, error) {
	return nil, serveAPI(ctx)
}
