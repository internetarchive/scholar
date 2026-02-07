package blobproc

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/miku/blobproc/dedent"
)

func TestURLMap(t *testing.T) {
	f, err := os.CreateTemp("", "blobproc-test-urlmap-*")
	if err != nil {
		t.Fatalf("failed to create temp db for test: %s", err)
	}
	t.Logf("%s", f.Name())
	defer os.Remove(f.Name())
	u := &URLMap{Path: f.Name()}
	if err := u.EnsureDB(); err != nil {
		t.Fatalf("could not create db: %v", err)
	}
	if err := u.Insert("123", "123"); err != nil {
		t.Fatalf("could not insert into db: %v", err)
	}
	s, err := renderTable(f.Name())
	if err != nil {
		t.Fatalf("failed to render table: %s", err)
	}
	t.Log("✅\n" + s)
}

func renderTable(path string) (string, error) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return "", err
	}
	cmd := exec.Command("sqlite3", path)
	cmd.Stdin = strings.NewReader(dedent.Dedent(`
		.mode table
		select * from map;
	`))
	b, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return string(b), nil
}
