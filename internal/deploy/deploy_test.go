package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(filepath.Join(t.TempDir(), "deploys.json"))
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{
		"bierreel":                           "bierreel",
		"transpaclean/TranspaBridge":         "transpabridge",
		"blauweschuit/de-website-en-backend": "de-website-en-backend",
		"My Project 2":                       "my-project-2",
		"__weird__":                          "weird",
		"":                                   "",
	} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHostnameUsesTheProjectName(t *testing.T) {
	if got := Hostname("transpaclean/TranspaBridge", "loods.site"); got != "transpabridge.loods.site" {
		t.Errorf("= %q", got)
	}
	if got := Hostname("", "loods.site"); got != "" {
		t.Errorf("a project without a name should have no hostname, got %q", got)
	}
}

func TestPutKeepsWhatItIsNotChanging(t *testing.T) {
	s := newStore(t)
	if _, err := s.Put("g/app", func(d *Deploy) error {
		d.Host, d.Kind, d.Dir = "app.loods.site", KindStatic, "/builds/app"
		d.Status = StatusLive
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// A rebuild: the status changes, the directory that is being served does not.
	d, err := s.Put("g/app", func(d *Deploy) error {
		d.Status = StatusBuilding
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Dir != "/builds/app" {
		t.Fatalf("Dir = %q, want the last good build to stay", d.Dir)
	}
	if !d.Live() {
		t.Fatal("a rebuilding deploy with a previous build is still live")
	}
	if len(s.List()) != 1 {
		t.Fatalf("Put made a second record: %+v", s.List())
	}
}

func TestPutRefusesADeployWithoutAHostname(t *testing.T) {
	s := newStore(t)
	if _, err := s.Put("g/app", func(d *Deploy) error { return nil }); err == nil {
		t.Fatal("a deploy with no hostname was accepted")
	}
}

func TestRemove(t *testing.T) {
	s := newStore(t)
	s.Put("g/app", func(d *Deploy) error { d.Host = "app.loods.site"; return nil })

	if _, found, _ := s.Remove("g/app"); !found {
		t.Fatal("Remove did not find the deploy")
	}
	if _, found, _ := s.Remove("g/app"); found {
		t.Fatal("Remove found it twice")
	}
	if len(s.List()) != 0 {
		t.Fatal("the deploy is still in the file")
	}
}

// The edge reads this file. Its fields have to be at the top level of each
// entry, or every deploy silently stops being routed.
func TestTheFileIsWhatTheEdgeReads(t *testing.T) {
	s := newStore(t)
	s.Put("g/app", func(d *Deploy) error {
		d.Host, d.Dir, d.SPA, d.Kind = "app.loods.site", "/builds/app", true, KindStatic
		return nil
	})

	b, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"routes"`, `"host": "app.loods.site"`, `"dir": "/builds/app"`, `"spa": true`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("deploys.json does not contain %s:\n%s", want, b)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

func TestFreePortKeepsAProjectsPort(t *testing.T) {
	s := newStore(t)
	p1, err := s.FreePort("g/app", nil)
	if err != nil {
		t.Fatal(err)
	}
	if p1 < FirstPort || p1 > LastPort {
		t.Fatalf("port %d is outside the deploy range", p1)
	}
	s.Put("g/app", func(d *Deploy) error { d.Host, d.Port = "app.loods.site", p1; return nil })

	// Another project may not have it …
	p2, _ := s.FreePort("g/other", nil)
	if p2 == p1 {
		t.Fatalf("two projects were given port %d", p1)
	}
	// … and a redeploy of the first keeps it, so nothing that remembered the
	// port is sent somewhere else.
	again, _ := s.FreePort("g/app", nil)
	if again != p1 {
		t.Fatalf("redeploy moved the port from %d to %d", p1, again)
	}
}

func TestFreePortSkipsWhatIsAlreadyListening(t *testing.T) {
	s := newStore(t)
	busy := map[int]bool{FirstPort: true, FirstPort + 1: true}
	p, err := s.FreePort("g/app", func(port int) bool { return busy[port] })
	if err != nil {
		t.Fatal(err)
	}
	if p != FirstPort+2 {
		t.Fatalf("port = %d, want the first one nothing holds", p)
	}
}

// Resolve

func projectDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		full := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestResolveDetectsAStaticSite(t *testing.T) {
	dir := projectDir(t, map[string]string{
		"package.json": `{"scripts":{"build":"vite build","dev":"vite"}}`,
		"dist/.keep":   "",
	})
	r, err := Resolve(Config{}, "bierreel", dir)
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != KindStatic {
		t.Fatalf("kind = %q", r.Kind)
	}
	if r.Build != "npm run build" {
		t.Errorf("build = %q", r.Build)
	}
	if r.Serve != filepath.Join(dir, "dist") {
		t.Errorf("serve = %q", r.Serve)
	}
	if !r.SPA {
		t.Error("a bundled site defaults to SPA fallback")
	}
	if r.Host != "bierreel."+DefaultDomain {
		t.Errorf("host = %q", r.Host)
	}
}

// A project that builds *and* has a start script is a server, not a directory:
// publishing only its build output would quietly drop everything the server does.
func TestResolveDetectsAServerBeforeADirectory(t *testing.T) {
	dir := projectDir(t, map[string]string{
		"package.json": `{"scripts":{"build":"vite build","start":"node build"}}`,
	})
	r, err := Resolve(Config{}, "app", dir)
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != KindProc {
		t.Fatalf("kind = %q, want proc", r.Kind)
	}
	if r.Build != "npm run build" || r.Run != "npm start" {
		t.Errorf("build = %q, run = %q", r.Build, r.Run)
	}
}

func TestResolveRefusesWhatItCannotRecognise(t *testing.T) {
	dir := projectDir(t, map[string]string{"README.md": "hello"})
	_, err := Resolve(Config{}, "notes", dir)
	if err == nil {
		t.Fatal("an unrecognisable project was published anyway")
	}
	if !strings.Contains(err.Error(), "deploys.yaml") {
		t.Errorf("the error should say where to fix it: %v", err)
	}
}

func TestConfigOverridesDetection(t *testing.T) {
	dir := projectDir(t, map[string]string{
		"package.json": `{"scripts":{"build":"vite build"}}`,
		"dist/.keep":   "",
	})
	no := false
	cfg := Config{
		Site: Site{Domain: "example.test"},
		Deploys: map[string]ProjectSpec{
			"bierreel": {Sub: "beer", Build: "bun run build", Dir: "out", SPA: &no},
		},
	}
	r, err := Resolve(cfg, "bierreel", dir)
	if err != nil {
		t.Fatal(err)
	}
	if r.Host != "beer.example.test" {
		t.Errorf("host = %q", r.Host)
	}
	if r.Build != "bun run build" {
		t.Errorf("build = %q", r.Build)
	}
	if r.Serve != filepath.Join(dir, "out") {
		t.Errorf("serve = %q", r.Serve)
	}
	if r.SPA {
		t.Error("spa: false in the config was ignored")
	}
}

func TestResolveComposeNeedsAServiceAndPort(t *testing.T) {
	dir := projectDir(t, map[string]string{"compose.yaml": "services: {}"})
	if _, err := Resolve(Config{}, "stack", dir); err == nil {
		t.Fatal("a compose deploy without a service was accepted")
	}
	cfg := Config{Deploys: map[string]ProjectSpec{"stack": {Service: "web", Port: 80}}}
	r, err := Resolve(cfg, "stack", dir)
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != KindCompose || r.Build != "docker compose up -d --build web" {
		t.Fatalf("%+v", r)
	}
}

func TestOutputIsCheckedAfterTheBuild(t *testing.T) {
	dir := projectDir(t, map[string]string{"package.json": `{"scripts":{"build":"true"}}`})
	r, err := Resolve(Config{Deploys: map[string]ProjectSpec{"app": {Kind: KindStatic, Dir: "dist"}}}, "app", dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Output(); err == nil {
		t.Fatal("a directory that does not exist was accepted as a build")
	}

	os.MkdirAll(filepath.Join(dir, "dist"), 0o755)
	if _, err := r.Output(); err == nil {
		t.Fatal("a directory without an index.html was accepted as a site")
	}

	os.WriteFile(filepath.Join(dir, "dist", "index.html"), []byte("x"), 0o644)
	out, err := r.Output()
	if err != nil || out != filepath.Join(dir, "dist") {
		t.Fatalf("out = %q, err = %v", out, err)
	}
}
