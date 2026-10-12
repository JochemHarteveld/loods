package proc

import (
	"bytes"
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
	m := NewManager(filepath.Join(t.TempDir(), "procs.json"), func() {})
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
	m := NewManager(filepath.Join(t.TempDir(), "procs.json"), func() {})
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
	m1 := NewManager(state, func() {})
	spec := Spec{ID: "orphan", Name: "orphan", Dir: t.TempDir(), Run: `while :; do sleep 0.1; done`}
	if err := m1.Start(spec); err != nil {
		t.Fatal(err)
	}
	// A new manager (loods restarted) finds the still-running process.
	m2 := NewManager(state, func() {})
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
