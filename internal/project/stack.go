package project

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"loods/internal/fsx"
)

// DetectStack names the toolchains a project uses, from marker files in its
// root and its direct subfolders (monorepos like app/ + web/).
func DetectStack(dir string) []string {
	out := stackAt(dir)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || fsx.Noisy(e.Name()) {
			continue
		}
		for _, s := range stackAt(filepath.Join(dir, e.Name())) {
			if !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
	}
	return out
}

func stackAt(dir string) []string {
	var out []string
	add := func(ok bool, name string) {
		if ok {
			out = append(out, name)
		}
	}
	add(exists(dir, "pubspec.yaml"), "flutter")
	if b, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		pkg := string(b)
		switch {
		case strings.Contains(pkg, `"vscode"`) && strings.Contains(pkg, `"engines"`):
			out = append(out, "vscode-ext")
		case strings.Contains(pkg, `"@sveltejs/kit"`):
			out = append(out, "sveltekit")
		case strings.Contains(pkg, `"svelte"`):
			out = append(out, "svelte")
		case strings.Contains(pkg, `"next"`):
			out = append(out, "next")
		case strings.Contains(pkg, `"react"`):
			out = append(out, "react")
		default:
			out = append(out, "node")
		}
	}
	add(exists(dir, "go.mod"), "go")
	add(exists(dir, "Cargo.toml"), "rust")
	add(exists(dir, "pyproject.toml", "requirements.txt", "manage.py"), "python")
	add(exists(dir, "platformio.ini"), "platformio")
	add(exists(dir, "docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"), "docker")
	add(exists(dir, "firebase.json"), "firebase")
	add(exists(dir, "wrangler.toml", "wrangler.json", "wrangler.jsonc"), "cloudflare")
	return out
}
