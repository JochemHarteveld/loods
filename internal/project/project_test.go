package project

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDetectCommands(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string, mode os.FileMode) {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("dev-stack.sh", "#!/bin/sh\n", 0o755)
	write("docker-compose.yml", "services: {}\n", 0o644)
	write("app/pubspec.yaml", "name: app\n", 0o644)
	write("app/lib/main.dart", "void main() {}\n", 0o644)
	write("web/package.json", `{"scripts":{"dev":"vite"}}`, 0o644)
	write("web/pnpm-lock.yaml", "", 0o644)
	write("node_modules/x/package.json", `{"scripts":{"dev":"x"}}`, 0o644)

	var got []string
	for _, c := range DetectCommands(dir) {
		got = append(got, c.Name+" = "+c.Run+" @"+c.Dir)
	}
	want := []string{
		"dev-stack = ./dev-stack.sh @",
		"compose = docker compose up @",
		"app flutter = flutter run @app",
		"web dev = pnpm run dev @web",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestSessionProjectFallsBackToName(t *testing.T) {
	items := []*Item{{Rel: "b/barsys", Name: "barsys", Path: "/nonexistent/P/b/barsys"}, {Rel: "t/kit", Name: "kit", Path: "/nonexistent/P/t/kit"}}
	for dir, want := range map[string]string{
		"/nonexistent/P/barsys":          "b/barsys",
		"/nonexistent/P/t/old/kit/tools": "t/kit",
		"/nonexistent/P/t/workspace":     "",
		"/nonexistent/P/b/barsys/app":    "b/barsys",
	} {
		got := ""
		if it := ContainingOrRenamed(items, dir); it != nil {
			got = it.Rel
		}
		if got != want {
			t.Errorf("%s → %q, want %q", dir, got, want)
		}
	}
}

// The two standard Go layouts: main.go in the root, or one package per command
// under cmd/ — which is loods's own layout, so its card keeps a run command.
func TestDetectGoCommands(t *testing.T) {
	root := t.TempDir()
	write := func(rel string) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("package main\n"), 0o644)
	}
	write("flat/go.mod")
	write("flat/main.go")
	write("layered/go.mod")
	write("layered/cmd/tool/main.go")
	write("layered/cmd/helper/main.go")

	for dir, want := range map[string][]string{
		"flat":    {"go run = go run ."},
		"layered": {"go run helper = go run ./cmd/helper", "go run tool = go run ./cmd/tool"},
	} {
		var got []string
		for _, c := range DetectCommands(filepath.Join(root, dir)) {
			got = append(got, c.Name+" = "+c.Run)
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("%s: got %q, want %q", dir, got, want)
		}
	}
}
