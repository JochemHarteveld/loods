package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"loods/internal/fsx"
)

// Command is something a project can run in the Garage.
type Command struct {
	Name   string `json:"name"`
	Run    string `json:"run"`            // shell command, run with bash -lc
	Dir    string `json:"dir,omitempty"`  // relative to the project
	Source string `json:"source"`         // where it was found: package.json, pubspec.yaml, config, …
	Keys   bool   `json:"keys,omitempty"` // takes single-key input (flutter: r = hot reload, R = hot restart)
	URL    string `json:"url,omitempty"`  // fixed URL to open; otherwise taken from the output
}

// DetectCommands finds runnable commands in a project root and its direct
// subfolders (monorepos: app/, web/, dashboard/).
func DetectCommands(dir string) []Command {
	out := commandsAt(dir, "")
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || fsx.Noisy(e.Name()) {
			continue
		}
		out = append(out, commandsAt(filepath.Join(dir, e.Name()), e.Name())...)
	}
	return out
}

// A detector looks at one folder and adds what it knows how to start. Adding
// support for another toolchain is one function and one line in detectors,
// which is why they are a list and not a long switch.
type detector func(dir string, add addFunc)

// addFunc takes a command as the detector found it; the folder it was found in
// is filled in by commandsAt.
type addFunc func(name, run, source string, keys bool)

var detectors = []detector{devScripts, nodeScripts, flutterApp, goMain, compose, platformIO, django, makefileTarget}

func commandsAt(dir, sub string) []Command {
	var out []Command
	add := func(name, run, source string, keys bool) {
		if sub != "" {
			name = sub + " " + name
		}
		out = append(out, Command{Name: name, Run: run, Dir: sub, Source: source, Keys: keys})
	}
	for _, d := range detectors {
		d(dir, add)
	}
	return out
}

func exists(dir string, names ...string) bool {
	for _, n := range names {
		if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
			return true
		}
	}
	return false
}

// devScripts: an executable dev script in the project itself.
func devScripts(dir string, add addFunc) {
	for _, s := range []string{"dev-stack.sh", "dev.sh"} {
		if info, err := os.Stat(filepath.Join(dir, s)); err == nil && info.Mode()&0o111 != 0 {
			add(strings.TrimSuffix(s, ".sh"), "./"+s, s, false)
		}
	}
}

func nodeScripts(dir string, add addFunc) {
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(b, &pkg) != nil {
		return
	}
	pm := PackageManager(dir)
	switch {
	case pkg.Scripts["dev"] != "":
		add("dev", pm+" run dev", "package.json", false)
	case pkg.Scripts["start"] != "":
		add("start", pm+" start", "package.json", false)
	}
}

func flutterApp(dir string, add addFunc) {
	if exists(dir, "pubspec.yaml") && exists(dir, "lib/main.dart") {
		add("flutter", "flutter run", "pubspec.yaml", true)
	}
}

func goMain(dir string, add addFunc) {
	if !exists(dir, "go.mod") {
		return
	}
	if exists(dir, "main.go") {
		add("go run", "go run .", "go.mod", false)
		return
	}
	// The other standard layout: one package per command under cmd/.
	entries, _ := os.ReadDir(filepath.Join(dir, "cmd"))
	for _, e := range entries {
		if e.IsDir() && exists(dir, filepath.Join("cmd", e.Name(), "main.go")) {
			add("go run "+e.Name(), "go run ./cmd/"+e.Name(), "go.mod", false)
		}
	}
}

func compose(dir string, add addFunc) {
	for _, f := range []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"} {
		if exists(dir, f) {
			add("compose", "docker compose up", f, false)
			return
		}
	}
}

func platformIO(dir string, add addFunc) {
	if exists(dir, "platformio.ini") {
		add("upload", "pio run -t upload", "platformio.ini", false)
	}
}

func django(dir string, add addFunc) {
	if exists(dir, "manage.py") {
		add("django", "python3 manage.py runserver", "manage.py", false)
	}
}

var makeTarget = regexp.MustCompile(`(?m)^(dev|run|serve):`)

func makefileTarget(dir string, add addFunc) {
	b, err := os.ReadFile(filepath.Join(dir, "Makefile"))
	if err != nil {
		return
	}
	if m := makeTarget.FindSubmatch(b); m != nil {
		add("make "+string(m[1]), "make "+string(m[1]), "Makefile", false)
	}
}

// PackageManager picks npm/pnpm/yarn/bun from the lockfile next to package.json
// or one level up (workspace root).
func PackageManager(dir string) string {
	for _, d := range []string{dir, filepath.Dir(dir)} {
		for lock, pm := range map[string]string{"pnpm-lock.yaml": "pnpm", "yarn.lock": "yarn", "bun.lockb": "bun", "bun.lock": "bun"} {
			if _, err := os.Stat(filepath.Join(d, lock)); err == nil {
				return pm
			}
		}
	}
	return "npm"
}
