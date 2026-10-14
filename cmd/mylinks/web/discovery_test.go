package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mikaelstaldal/go-server-common/auth"
	"github.com/mikaelstaldal/mylinks/cmd/mylinks/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestDiscovery(t *testing.T) {
	database, err := db.InitDB(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	app := newHandlers("../../..", database, "", true).Routes()
	passwordFile := filepath.Join(t.TempDir(), "htpasswd")
	require.NoError(t, os.WriteFile(passwordFile, []byte("alice:$2a$10$g6h.kOLcUo4A7Yg9X1hPGeCJT523p2.6xNk9Yldg2iyqz2F7U2o9e\n"), 0600))
	passwords, err := auth.LoadHtpasswdStrict(passwordFile, nil)
	require.NoError(t, err)
	document, err := os.ReadFile("../../../openapi.yaml")
	require.NoError(t, err)
	for _, authenticated := range []bool{false, true} {
		t.Run(map[bool]string{false: "public", true: "basic auth"}[authenticated], func(t *testing.T) {
			next := app
			if authenticated {
				next = passwords.Middleware("mylinks")(next)
			}
			handler := APIHeaders(Discovery(next))
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				for _, path := range []string{"/openapi.yaml", "/.well-known/api-catalog"} {
					response := httptest.NewRecorder()
					handler.ServeHTTP(response, httptest.NewRequest(method, path, nil))
					require.Equal(t, http.StatusOK, response.Code)
					assert.Contains(t, response.Header().Values("Link"), `</openapi.yaml>; rel="service-desc"`)
					if method == http.MethodHead {
						assert.Empty(t, response.Body.Bytes())
					}
					if path == "/openapi.yaml" {
						assert.Equal(t, "application/yaml", response.Header().Get("Content-Type"))
						if method == http.MethodGet {
							assert.Equal(t, document, response.Body.Bytes())
						}
					} else {
						assert.Equal(t, `application/linkset+json; profile="https://www.rfc-editor.org/info/rfc9727"`, response.Header().Get("Content-Type"))
						assert.Contains(t, response.Header().Values("Link"), `</.well-known/api-catalog>; rel="api-catalog"`)
						if method == http.MethodGet {
							var catalog any
							require.NoError(t, json.Unmarshal(response.Body.Bytes(), &catalog))
							assert.JSONEq(t, `{"linkset":[{"anchor":"/","service-desc":[{"href":"/openapi.yaml","type":"application/yaml"}]}]}`, response.Body.String())
						}
					}
				}
			}
			for _, path := range []string{"/", "/999999", "/missing/path"} {
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
				assert.Contains(t, response.Header().Values("Link"), `</openapi.yaml>; rel="service-desc"`)
				if authenticated {
					assert.Equal(t, http.StatusUnauthorized, response.Code)
				} else if path != "/" {
					assert.GreaterOrEqual(t, response.Code, 400)
				}
			}
		})
	}
	var spec struct {
		OpenAPI string `yaml:"openapi"`
		Servers []struct {
			URL string `yaml:"url"`
		} `yaml:"servers"`
		Paths map[string]map[string]any `yaml:"paths"`
	}
	require.NoError(t, yaml.Unmarshal(document, &spec))
	require.Equal(t, "3.0.3", spec.OpenAPI)
	require.NotEmpty(t, spec.Servers)
	server, err := url.Parse(spec.Servers[0].URL)
	require.NoError(t, err)
	require.False(t, server.IsAbs())
	require.Empty(t, server.Host)
	require.Contains(t, spec.Paths["/"], "get")
	origin, err := url.Parse("https://mylinks.example/openapi.yaml")
	require.NoError(t, err)
	operation := origin.ResolveReference(server)
	operation.Path = strings.TrimRight(operation.Path, "/") + "/"
	response := httptest.NewRecorder()
	APIHeaders(Discovery(app)).ServeHTTP(response, httptest.NewRequest(http.MethodGet, operation.String(), nil))
	assert.Equal(t, http.StatusOK, response.Code)
}
