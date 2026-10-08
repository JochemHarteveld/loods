package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func status(m *Manager, id string) (string, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.getLocked(id)
	if p == nil {
		return "", 0
	}
	return p.Status, p.ExitCode
}

func TestManagerRunStopRestart(t *testing.T) {
	m := newManager(filepath.Join(t.TempDir(), "procs.json"), func() {})
	spec := Spec{ID: "demo#dev", Project: "demo", Name: "dev", Dir: t.TempDir(),
		Run: `echo "  ➜  Local:   http://localhost:5173/"; trap 'echo bye; exit 0' INT; while :; do sleep 0.1; done`}
	if err := m.Start(spec); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(spec); err == nil {
		t.Fatal("second start of a running command should fail")
	}

	backlog, ch, detach, ok := m.Attach(spec.ID)
	if !ok {
		t.Fatal("attach failed")
	}
	defer detach()
	out := append([]byte(nil), backlog...)
	waitFor(t, "output", func() bool {
		for {
			select {
			case b := <-ch:
				out = append(out, b...)
				continue
			default:
			}
			return bytes.Contains(out, []byte("Local:"))
		}
	})
	waitFor(t, "url detection", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return slices.Contains(m.getLocked(spec.ID).URLs, "http://localhost:5173/")
	})

	// Stop sends Ctrl+C through the pty; the trap exits cleanly.
	if err := m.Stop(spec.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "exit", func() bool { s, _ := status(m, spec.ID); return s == "exited" })
	if _, code := status(m, spec.ID); code != 0 {
		t.Fatalf("exit code %d, want 0 (Ctrl+C should reach the trap)", code)
	}
	m.mu.Lock()
	stopped := m.getLocked(spec.ID).Stopped
	m.mu.Unlock()
	if !stopped {
		t.Fatal("a process the user stopped should be marked stopped")
	}

	if err := m.Restart(spec.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "running again", func() bool { s, _ := status(m, spec.ID); return s == "running" })
	m.StopAll(5 * time.Second)
	if s, _ := status(m, spec.ID); s != "exited" {
		t.Fatalf("after StopAll status = %s", s)
	}
	if err := m.Remove(spec.ID); err != nil {
		t.Fatal(err)
	}
}

func TestStopEscalatesWhenCtrlCIsIgnored(t *testing.T) {
	m := newManager(filepath.Join(t.TempDir(), "procs.json"), func() {})
	spec := Spec{ID: "stubborn", Name: "stubborn", Dir: t.TempDir(), Run: `trap '' INT; while :; do sleep 0.1; done`}
	if err := m.Start(spec); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	m.Stop(spec.ID)
	m.Stop(spec.ID) // second stop while stopping = SIGKILL now
	waitFor(t, "killed", func() bool { s, _ := status(m, spec.ID); return s == "exited" })
}

func TestOrphanAdoption(t *testing.T) {
	state := filepath.Join(t.TempDir(), "procs.json")
	m1 := newManager(state, func() {})
	spec := Spec{ID: "orphan", Name: "orphan", Dir: t.TempDir(), Run: `while :; do sleep 0.1; done`}
	if err := m1.Start(spec); err != nil {
		t.Fatal(err)
	}
	// A new manager (loods restarted) finds the still-running process.
	m2 := newManager(state, func() {})
	if s, _ := status(m2, spec.ID); s != "orphan" {
		t.Fatalf("status = %q, want orphan", s)
	}
	if err := m2.Stop(spec.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "orphan gone", func() bool { s, _ := status(m2, spec.ID); return s == "exited" })
	waitFor(t, "first manager sees exit", func() bool { s, _ := status(m1, spec.ID); return s == "exited" })
}

func TestInspect(t *testing.T) {
	p := &Proc{}
	p.inspect([]byte("\x1b[32m  Local: \x1b[1mhttp://0.0.0.0:5173/\x1b[0m\r\n  served at http://loc"))
	p.inspect([]byte("alhost:8080.\r\nError: listen EADDRINUSE: address already in use :::3000"))
	want := []string{"http://localhost:5173/", "http://localhost:8080"}
	if !slices.Equal(p.URLs, want) {
		t.Errorf("URLs = %q, want %q", p.URLs, want)
	}
	if p.Warning == "" {
		t.Error("port conflict not detected")
	}
}

func TestAppendCapped(t *testing.T) {
	buf := appendCapped(nil, []byte(strings.Repeat("x", 90)+"\nline two\n"), 50)
	if string(buf) != "line two\n" {
		t.Errorf("got %q, want the backlog to start at a line boundary", buf)
	}
}

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
	for _, c := range detectCommands(dir) {
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

func TestApplyConfig(t *testing.T) {
	it := &Item{Rel: "g/p", Commands: []Command{{Name: "dev", Run: "npm run dev"}, {Name: "compose", Run: "docker compose up"}}}
	applyConfig([]*Item{it}, Config{Projects: map[string]ProjectConfig{"g/p": {
		Default: "web",
		Commands: map[string]CommandConfig{
			"compose": {Hide: true},
			"dev":     {Run: "npm run dev -- --port 4000"},
			"web":     {Run: "make web", URL: "http://localhost:4000"},
		},
	}}})
	var got []string
	for _, c := range it.Commands {
		got = append(got, c.Name+"="+c.Run)
	}
	if want := []string{"dev=npm run dev -- --port 4000", "web=make web"}; !slices.Equal(got, want) {
		t.Errorf("commands = %q, want %q", got, want)
	}
	if it.DefaultCommand != "web" {
		t.Errorf("default = %q", it.DefaultCommand)
	}
}

func TestResyncReplacesFullQueue(t *testing.T) {
	ch := make(chan []byte, 2)
	ch <- []byte("a")
	ch <- []byte("b")
	resync(ch, []byte("log"))
	if len(ch) != 1 {
		t.Fatalf("queue has %d items, want 1", len(ch))
	}
	if got := string(<-ch); got != "\x1bclog" {
		t.Fatalf("got %q, want reset + backlog", got)
	}
}
