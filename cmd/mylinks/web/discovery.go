package web

import (
	"bytes"
	"net/http"
	"time"

	mylinks "github.com/mikaelstaldal/mylinks"
)

// APIHeaders advertises the description on every response, including middleware errors.
func APIHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Link", `</openapi.yaml>; rel="service-desc"`)
		next.ServeHTTP(w, r)
	})
}

// Discovery serves public API metadata before the authenticated application.
// Keep host validation outside this handler, as for all other resources.
func Discovery(next http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		http.ServeContent(w, r, "openapi.yaml", time.Time{}, bytes.NewReader(mylinks.OpenAPI))
	})
	mux.HandleFunc("GET /.well-known/api-catalog", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", `application/linkset+json; profile="https://www.rfc-editor.org/info/rfc9727"`)
		w.Header().Add("Link", `</.well-known/api-catalog>; rel="api-catalog"`)
		if r.Method != http.MethodHead {
			_, _ = w.Write([]byte(`{"linkset":[{"anchor":"/","service-desc":[{"href":"/openapi.yaml","type":"application/yaml"}]}]}`))
		}
	})
	mux.Handle("/", next)
	return commonHeaders(mux)
}
