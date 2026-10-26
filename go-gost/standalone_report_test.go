package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStandaloneReportConfiguration(t *testing.T) {
	if err := configureStandaloneReporting(true, ""); err != nil {
		t.Fatal(err)
	}
	if err := configureStandaloneReporting(false, "unused"); err == nil {
		t.Fatal("accepted a second reporting identity in managed mode")
	}
	path := filepath.Join(t.TempDir(), "report.json")
	for _, content := range []string{`{}`, `{"addr":"127.0.0.1:1"}`, `{"secret":"test"}`} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if err := configureStandaloneReporting(true, path); err == nil {
			t.Fatalf("accepted incomplete configuration: %s", content)
		}
	}
	if err := os.WriteFile(path, []byte(`{"addr":"127.0.0.1:1","secret":"test"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := configureStandaloneReporting(true, path); err != nil {
		t.Fatal(err)
	}
}
