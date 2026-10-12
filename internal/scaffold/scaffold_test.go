package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewProjectDir(t *testing.T) {
	root := t.TempDir()
	rel, path, err := Dir(root, "", "huizenzoeker")
	if err != nil {
		t.Fatal(err)
	}
	if rel != "huizenzoeker" || path != filepath.Join(root, "huizenzoeker") {
		t.Fatalf("rel %q path %q", rel, path)
	}
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		t.Fatalf("folder not created: %v", err)
	}
	if _, _, err := Dir(root, "", "huizenzoeker"); err == nil {
		t.Error("an existing folder was taken over")
	}
	rel, _, err = Dir(root, "blauweschuit", "barsys2")
	if err != nil || rel != "blauweschuit/barsys2" {
		t.Fatalf("group: rel %q err %v", rel, err)
	}
	for _, name := range []string{"", "Huizen Zoeker", "../escape", "a/b", strings.Repeat("x", 33)} {
		if _, _, err := Dir(root, "", name); err == nil {
			t.Errorf("name %q was accepted", name)
		}
	}
}
