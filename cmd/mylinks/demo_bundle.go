package main

import (
	"fmt"
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	"github.com/mikaelstaldal/mylinks/cmd/mylinks/db"
	"github.com/mikaelstaldal/mylinks/cmd/mylinks/web"
	"github.com/mikaelstaldal/mylinks/ui"
)

// writeDemoBundle writes the normal MyLinks page and assets with a browser-local backend.
func writeDemoBundle(out string) error {
	entries, err := os.ReadDir(out)
	if err == nil && len(entries) != 0 {
		return fmt.Errorf("%s is not empty", out)
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	tmp, err := os.MkdirTemp("", "mylinks-demo-*")
	if err != nil {
		return err
	}
	defer func(path string) {
		_ = os.RemoveAll(path)
	}(tmp)
	database, err := db.InitDB(filepath.Join(tmp, databaseName))
	if err != nil {
		return err
	}
	defer database.Close()
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	web.NewHandlers("", database, "").Routes().ServeHTTP(rec, req)
	if rec.Code != 200 {
		return fmt.Errorf("render demo page: HTTP %d", rec.Code)
	}
	if err := os.MkdirAll(filepath.Join(out, "static"), 0755); err != nil {
		return err
	}
	page := strings.Replace(rec.Body.String(), "</body>", `<script src="./static/demo-client.js" defer></script></body>`, 1)
	if err := os.WriteFile(filepath.Join(out, "index.html"), []byte(page), 0644); err != nil {
		return err
	}
	for _, name := range []string{"demo-sw.js"} {
		data, err := fs.ReadFile(ui.Files, "static/"+name)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(out, name), data, 0644); err != nil {
			return err
		}
	}
	return fs.WalkDir(ui.Files, "static", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(ui.Files, path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(out, path), data, 0644)
	})
}
