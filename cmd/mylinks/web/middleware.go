package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func commonHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; form-action 'self'; base-uri 'self'; frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "deny")

		next.ServeHTTP(w, r)
	})
}

// Only representation selection needs context; all operation inputs and outputs
// use the generated types. Accept matching deliberately follows the legacy API.
type jsonRequestKey struct{}

func wantsJSON(ctx context.Context) bool {
	value, _ := ctx.Value(jsonRequestKey{}).(bool)
	return value
}

// prepareAPIRequest preserves net/http's form limits and first-value semantics
// before handing decoding and response writing to the generated server.
func prepareAPIRequest(server http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Pattern, "{id}") {
			if _, err := parseID(r.PathValue("id")); err != nil {
				handleAPIError(r.Context(), w, r, err)
				return
			}
		}
		if r.Method == http.MethodPost || r.Method == http.MethodPatch {
			if err := r.ParseForm(); err != nil {
				sendError(w, fmt.Sprintf("Failed to parse form: %v", err), http.StatusBadRequest)
				return
			}
		}
		request := r.Clone(context.WithValue(r.Context(), jsonRequestKey{}, wantJson(r)))
		if request.Method == http.MethodHead {
			request.Method = http.MethodGet
		}
		request.URL.RawQuery = firstValues(r.URL.Query()).Encode()
		// The application middleware has already checked configured authentication.
		// With auth disabled, malformed Authorization headers are ignored as before.
		request.Header.Del("Authorization")
		request.Header.Set("HX-Request", strconv.FormatBool(r.Header.Get("HX-Request") == "true"))
		if request.Method == http.MethodPost || request.Method == http.MethodPatch {
			request.PostForm = firstValues(r.PostForm)
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.ContentLength = -1
			request.Body = http.NoBody
		}
		server.ServeHTTP(apiHeadersWriter{ResponseWriter: w}, request)
	}
}

func firstValues(values url.Values) url.Values {
	first := make(url.Values, len(values))
	for key, entries := range values {
		if len(entries) != 0 {
			first[key] = entries[:1]
		}
	}
	return first
}

// ogen adds a charset and CORS exposure to generated responses. Keep the
// established headers; response bodies and status codes are written by ogen.
type apiHeadersWriter struct{ http.ResponseWriter }

func (w apiHeadersWriter) WriteHeader(status int) {
	switch w.Header().Get("Content-Type") {
	case "text/html; charset=utf-8":
		w.Header().Set("Content-Type", "text/html")
	case "application/json; charset=utf-8":
		w.Header().Set("Content-Type", "application/json")
	}
	w.Header().Del("Access-Control-Expose-Headers")
	w.ResponseWriter.WriteHeader(status)
}

func (w apiHeadersWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
