package web

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSendErrorEscapesTerminalControlsInLog(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previous) })

	response := httptest.NewRecorder()
	sendError(response, "fetch failed: \x1b[31mexample\u202e", http.StatusInternalServerError)

	assert.Contains(t, output.String(), `fetch failed: \u001b[31mexample\u202e`)
	assert.NotContains(t, output.String(), "\x1b")
	assert.NotContains(t, output.String(), "\u202e")
	assert.Equal(t, http.StatusInternalServerError, response.Code)
	assert.NotContains(t, response.Body.String(), "example")
}
