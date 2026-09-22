package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDemoBundle(t *testing.T) {
	out := filepath.Join(t.TempDir(), "site")
	if err := writeDemoBundle(out); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"index.html", "demo-sw.js", "static/demo-client.js", "static/style.7.css"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	page, err := os.ReadFile(filepath.Join(out, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page), "static/demo-client.js") || !strings.Contains(string(page), "Save Link") {
		t.Fatal("demo page does not contain the interactive UI")
	}
	if err := writeDemoBundle(out); err == nil {
		t.Fatal("expected populated destination to be rejected")
	}
}
