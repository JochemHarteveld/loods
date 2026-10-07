package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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

// detectCommands finds runnable commands in a project root and its direct
// subfolders (monorepos: app/, web/, dashboard/).
func detectCommands(dir string) []Command {
	out := commandsAt(dir, "")
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || noisyDirs[e.Name()] {
			continue
		}
		out = append(out, commandsAt(filepath.Join(dir, e.Name()), e.Name())...)
	}
	return out
}

var makeTarget = regexp.MustCompile(`(?m)^(dev|run|serve):`)

func commandsAt(dir, sub string) []Command {
	var out []Command
	add := func(name, run, source string, keys bool) {
		if sub != "" {
			name = sub + " " + name
		}
		out = append(out, Command{Name: name, Run: run, Dir: sub, Source: source, Keys: keys})
	}
	exists := func(n string) bool {
		_, err := os.Stat(filepath.Join(dir, n))
		return err == nil
	}

	for _, s := range []string{"dev-stack.sh", "dev.sh"} {
		if info, err := os.Stat(filepath.Join(dir, s)); err == nil && info.Mode()&0o111 != 0 {
			add(strings.TrimSuffix(s, ".sh"), "./"+s, s, false)
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		var pkg struct {
			Scripts map[string]string `json:"scripts"`
		}
		if json.Unmarshal(b, &pkg) == nil {
			pm := packageManager(dir)
			switch {
			case pkg.Scripts["dev"] != "":
				add("dev", pm+" run dev", "package.json", false)
			case pkg.Scripts["start"] != "":
				add("start", pm+" start", "package.json", false)
			}
		}
	}
	if exists("pubspec.yaml") && exists("lib/main.dart") {
		add("flutter", "flutter run", "pubspec.yaml", true)
	}
	if exists("go.mod") && exists("main.go") {
		add("go run", "go run .", "go.mod", false)
	}
	for _, f := range []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"} {
		if exists(f) {
			add("compose", "docker compose up", f, false)
			break
		}
	}
	if exists("platformio.ini") {
		add("upload", "pio run -t upload", "platformio.ini", false)
	}
	if exists("manage.py") {
		add("django", "python3 manage.py runserver", "manage.py", false)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "Makefile")); err == nil {
		if m := makeTarget.FindSubmatch(b); m != nil {
			add("make "+string(m[1]), "make "+string(m[1]), "Makefile", false)
		}
	}
	return out
}

// packageManager picks npm/pnpm/yarn/bun from the lockfile next to package.json
// or one level up (workspace root).
func packageManager(dir string) string {
	for _, d := range []string{dir, filepath.Dir(dir)} {
		for lock, pm := range map[string]string{"pnpm-lock.yaml": "pnpm", "yarn.lock": "yarn", "bun.lockb": "bun", "bun.lock": "bun"} {
			if _, err := os.Stat(filepath.Join(d, lock)); err == nil {
				return pm
			}
		}
	}
	return "npm"
}
