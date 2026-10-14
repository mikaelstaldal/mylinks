package ui

import _ "embed"

// OpenAPI is the exact document used by ogen to generate the HTTP server.
//
//go:embed openapi.yaml
var OpenAPI []byte
