package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loods/internal/deploy"
	"loods/internal/project"
)

// deployServer is a board with one project, set up so publishing it runs a
// real build in a real process — the point of the test is that the pass after
// the build turns it into something the edge serves.
func deployServer(t *testing.T, files map[string]string, cfg string) (*Server, *project.Item) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))

	dir := filepath.Join(root, "app")
	for name, body := range files {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if cfg != "" {
		path := filepath.Join(root, "config", "loods", "deploys.yaml")
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	s := New(Options{
		Root: root, Archive: filepath.Join(root, "archive"), Depth: 3,
		StateFile: filepath.Join(root, "state", "procs.json"),
	})
	it := &project.Item{Rel: "app", Path: dir, Name: "app", Kind: project.KindGit}
	s.mu.Lock()
	s.items = []*project.Item{it}
	s.mu.Unlock()
	t.Cleanup(func() { s.procs.StopAll(2 * time.Second) })
	return s, it
}

// settle runs the pass that the watch loop runs, until the deploy stops moving.
func settle(t *testing.T, s *Server, rel string, want string) deploy.Deploy {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last deploy.Deploy
	for time.Now().Before(deadline) {
		s.tickDeploys()
		d, _ := s.deploys.Get(rel)
		last = d
		if d.Status == want {
			return d
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("deploy stayed %q (note %q), want %q", last.Status, last.Note, want)
	return last
}

func TestStaticDeployBuildsThenServes(t *testing.T) {
	s, it := deployServer(t, map[string]string{
		"package.json": `{"scripts":{"build":"mkdir -p dist && echo hello > dist/index.html"}}`,
	}, "site:\n  domain: test.site\n")

	d, err := s.publishDeploy(it)
	if err != nil {
		t.Fatal(err)
	}
	if d.Host != "app.test.site" {
		t.Fatalf("host = %q", d.Host)
	}
	if d.Status != deploy.StatusBuilding {
		t.Fatalf("status = %q, want it to start as building", d.Status)
	}

	live := settle(t, s, it.Rel, deploy.StatusLive)
	if live.Dir != filepath.Join(it.Path, "dist") {
		t.Fatalf("dir = %q, want what the build produced", live.Dir)
	}
	if !live.Live() {
		t.Fatal("a built deploy should be live")
	}
}

// The build is the deploy's only way of failing, and the page a visitor gets
// has to say what went wrong, so the note matters as much as the status.
func TestFailedBuildIsRecordedAndDoesNotTakeTheSiteDown(t *testing.T) {
	s, it := deployServer(t, map[string]string{
		"package.json": `{"scripts":{"build":"mkdir -p dist && echo hello > dist/index.html"}}`,
	}, "site:\n  domain: test.site\n")

	if _, err := s.publishDeploy(it); err != nil {
		t.Fatal(err)
	}
	good := settle(t, s, it.Rel, deploy.StatusLive)

	// Now break the build and publish again.
	os.WriteFile(filepath.Join(it.Path, "package.json"),
		[]byte(`{"scripts":{"build":"exit 3"}}`), 0o644)
	if _, err := s.publishDeploy(it); err != nil {
		t.Fatal(err)
	}
	failed := settle(t, s, it.Rel, deploy.StatusFailed)

	if !strings.Contains(failed.Note, "3") {
		t.Errorf("note = %q, want it to name the exit code", failed.Note)
	}
	if failed.Dir != good.Dir {
		t.Fatalf("a failed rebuild dropped the last good build: %q", failed.Dir)
	}
	if !failed.Live() {
		t.Fatal("a failed rebuild took the site offline")
	}
}

func TestProcDeployGetsAPortAndIsRoutedToIt(t *testing.T) {
	s, it := deployServer(t, map[string]string{"serve.sh": ""},
		"site:\n  domain: test.site\ndeploys:\n  app:\n    kind: proc\n    run: sleep 30\n")

	d, err := s.publishDeploy(it)
	if err != nil {
		t.Fatal(err)
	}
	if d.Port < deploy.FirstPort || d.Port > deploy.LastPort {
		t.Fatalf("port = %d, outside the deploy range", d.Port)
	}

	live := settle(t, s, it.Rel, deploy.StatusLive)
	want := "127.0.0.1:" + itoa(d.Port)
	if live.Upstream != want {
		t.Fatalf("upstream = %q, want %q", live.Upstream, want)
	}

	// Stopping it takes the route away, so the edge says "not running"
	// instead of timing out against a port nothing holds.
	s.procs.Stop(deployProcID(it.Rel))
	stopped := settle(t, s, it.Rel, deploy.StatusStopped)
	if stopped.Upstream != "" {
		t.Fatalf("upstream = %q, want it gone once the process is", stopped.Upstream)
	}
}

// A proc deploy that falls over is a different thing from one that was
// stopped, and the board has to tell them apart.
func TestProcDeployThatExitsNonZeroIsFailed(t *testing.T) {
	s, it := deployServer(t, map[string]string{"x": ""},
		"site:\n  domain: test.site\ndeploys:\n  app:\n    kind: proc\n    run: sh -c 'exit 9'\n")

	if _, err := s.publishDeploy(it); err != nil {
		t.Fatal(err)
	}
	failed := settle(t, s, it.Rel, deploy.StatusFailed)
	if !strings.Contains(failed.Note, "9") {
		t.Errorf("note = %q, want it to name the exit code", failed.Note)
	}
	if failed.Upstream != "" {
		t.Error("a dead process should not stay routed")
	}
}

func TestDeployCommandCarriesEnvAndPort(t *testing.T) {
	cmd := deployCommand(deploy.Recipe{
		Build: "npm run build",
		Run:   "node build",
		Env:   map[string]string{"NODE_ENV": "production", "TOKEN": "it's secret"},
	}, 8901)
	for _, want := range []string{
		"export NODE_ENV='production';",
		`export TOKEN='it'\''s secret';`,
		"export PORT=8901;",
		"npm run build && exec node build",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("command does not contain %s:\n%s", want, cmd)
		}
	}
}

func TestDeployCommandOfAStaticSiteIsJustTheBuild(t *testing.T) {
	cmd := deployCommand(deploy.Recipe{Kind: deploy.KindStatic, Build: "npm run build"}, 0)
	if cmd != "npm run build" {
		t.Fatalf("cmd = %q", cmd)
	}
	if got := deployCommand(deploy.Recipe{Kind: deploy.KindStatic}, 0); got != "" {
		t.Fatalf("a deploy with nothing to do should have no command, got %q", got)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
