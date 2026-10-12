package edge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// write puts a routes file on disk and returns a server reading it.
func serverWith(t *testing.T, routes ...Route) (*Server, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "deploys.json")
	st := NewStore(path)
	if err := st.Write(routes); err != nil {
		t.Fatal(err)
	}
	return NewServer(st), path
}

// get asks the server for one URL as a browser navigating would.
func get(t *testing.T, s *Server, host, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = host
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	return w
}

func staticDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestStaticServesAndFallsBack(t *testing.T) {
	dir := staticDir(t, map[string]string{
		"index.html":     "the app",
		"assets/app.js":  "console.log(1)",
		"nested/one.txt": "one",
	})
	s, _ := serverWith(t, Route{Host: "app.loods.site", Dir: dir, SPA: true})

	for _, tc := range []struct {
		name, path, want string
		code             int
	}{
		{"index", "/", "the app", 200},
		{"asset", "/assets/app.js", "console.log(1)", 200},
		{"nested file", "/nested/one.txt", "one", 200},
		{"client route falls back", "/settings/profile", "the app", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := get(t, s, "app.loods.site", tc.path)
			if w.Code != tc.code {
				t.Fatalf("code = %d, want %d", w.Code, tc.code)
			}
			if got := w.Body.String(); !strings.Contains(got, tc.want) {
				t.Fatalf("body = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

// A missing asset must not get index.html back: a browser that asked for
// JavaScript and is handed HTML fails in a way that is much harder to read
// than a 404.
func TestStaticMissingAssetIsNotTheIndex(t *testing.T) {
	dir := staticDir(t, map[string]string{"index.html": "the app"})
	s, _ := serverWith(t, Route{Host: "app.loods.site", Dir: dir, SPA: true})

	req := httptest.NewRequest(http.MethodGet, "/assets/gone.js", nil)
	req.Host = "app.loods.site"
	req.Header.Set("Sec-Fetch-Mode", "no-cors") // how a <script> asks
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", w.Code)
	}
	if strings.Contains(w.Body.String(), "the app") {
		t.Fatal("a missing asset was answered with index.html")
	}
}

func TestStaticWithoutSPAStillFourOhFours(t *testing.T) {
	dir := staticDir(t, map[string]string{"index.html": "the app"})
	s, _ := serverWith(t, Route{Host: "docs.loods.site", Dir: dir})

	w := get(t, s, "docs.loods.site", "/deep/link")
	if w.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", w.Code)
	}
}

func TestProxyForwardsAndKeepsTheVisitorsHost(t *testing.T) {
	var gotHost, gotProto, gotPath string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost, gotProto, gotPath = r.Host, r.Header.Get("X-Forwarded-Proto"), r.URL.Path
		w.Write([]byte("from the app"))
	}))
	defer backend.Close()
	u, _ := url.Parse(backend.URL)

	s, _ := serverWith(t, Route{Host: "api.loods.site", Upstream: u.Host})

	req := httptest.NewRequest(http.MethodGet, "/users/4", nil)
	req.Host = "api.loods.site"
	req.Header.Set("X-Forwarded-Proto", "https") // what Cloudflare sets
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "from the app") {
		t.Fatalf("code = %d, body = %q", w.Code, w.Body.String())
	}
	if gotHost != "api.loods.site" {
		t.Errorf("backend saw Host %q, want the visitor's hostname", gotHost)
	}
	if gotProto != "https" {
		t.Errorf("backend saw X-Forwarded-Proto %q, want https", gotProto)
	}
	if gotPath != "/users/4" {
		t.Errorf("backend saw path %q", gotPath)
	}
}

// 503 rather than 502 is deliberate: Cloudflare swaps an origin 502 or 504 for
// a generic page, which would throw away exactly the detail this page exists
// to show.
func TestProxyToNothingExplainsItself(t *testing.T) {
	s, _ := serverWith(t, Route{
		Host: "api.loods.site", Project: "group/api", Upstream: "127.0.0.1:1", // nothing listens
	})

	w := get(t, s, "api.loods.site", "/")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"api.loods.site", "127.0.0.1:1", "loods deploy log -p group/api"} {
		if !strings.Contains(body, want) {
			t.Errorf("the 502 page does not mention %q", want)
		}
	}
}

// A rebuild that fails must not take the running version down: what is there
// keeps being served, and the failure is the board's problem to show.
func TestFailedRebuildKeepsServingTheLastBuild(t *testing.T) {
	dir := staticDir(t, map[string]string{"index.html": "the good build"})
	s, _ := serverWith(t, Route{
		Host: "app.loods.site", Dir: dir, Status: "failed", Note: "npm run build exited 1",
	})

	w := get(t, s, "app.loods.site", "/")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "the good build") {
		t.Fatalf("code = %d, body = %q", w.Code, w.Body.String())
	}
}

func TestKnownHostThatIsNotServing(t *testing.T) {
	s, _ := serverWith(t, Route{
		Host: "app.loods.site", Project: "g/app", Status: "failed", Note: "npm run build exited 1",
	})

	w := get(t, s, "app.loods.site", "/")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"failed", "npm run build exited 1", "loods deploy -p g/app"} {
		if !strings.Contains(body, want) {
			t.Errorf("the 503 page does not mention %q", want)
		}
	}
}

// The wildcard record makes every name under the domain resolve, so an unknown
// host is the common mistake and the page has to help.
func TestUnknownHostListsWhatIsLive(t *testing.T) {
	dir := staticDir(t, map[string]string{"index.html": "x"})
	s, _ := serverWith(t,
		Route{Host: "app.loods.site", Dir: dir},
		Route{Host: "bierreel.loods.site", Dir: dir},
	)

	w := get(t, s, "typo.loods.site", "/")
	if w.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"typo.loods.site", "app.loods.site", "bierreel.loods.site"} {
		if !strings.Contains(body, want) {
			t.Errorf("the 404 page does not mention %q", want)
		}
	}
}

func TestEmptyTableSaysHowToDeploy(t *testing.T) {
	s, _ := serverWith(t)
	w := get(t, s, "anything.loods.site", "/")
	if w.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", w.Code)
	}
	if !strings.Contains(w.Body.String(), "loods deploy") {
		t.Error("an empty edge should say how to publish something")
	}
}

func TestHostIsMatchedWithoutPortOrCase(t *testing.T) {
	dir := staticDir(t, map[string]string{"index.html": "the app"})
	s, _ := serverWith(t, Route{Host: "App.Loods.Site", Dir: dir})

	for _, host := range []string{"app.loods.site", "APP.LOODS.SITE:443", "app.loods.site."} {
		w := get(t, s, host, "/")
		if w.Code != http.StatusOK {
			t.Errorf("%s: code = %d, want 200", host, w.Code)
		}
	}
}

func TestRefreshPicksUpANewDeploy(t *testing.T) {
	dir := staticDir(t, map[string]string{"index.html": "the app"})
	s, path := serverWith(t, Route{Host: "one.loods.site", Dir: dir})

	if w := get(t, s, "two.loods.site", "/"); w.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404 before the deploy exists", w.Code)
	}

	// What `loods deploy` will do: rewrite the file.
	st := NewStore(path)
	if err := st.Write([]Route{
		{Host: "one.loods.site", Dir: dir},
		{Host: "two.loods.site", Dir: dir},
	}); err != nil {
		t.Fatal(err)
	}

	if !s.Refresh() {
		t.Fatal("Refresh did not notice the rewritten routes file")
	}
	if w := get(t, s, "two.loods.site", "/"); w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 after the deploy exists", w.Code)
	}
	if hosts := s.Hosts(); len(hosts) != 2 || hosts[0].Host != "one.loods.site" {
		t.Fatalf("Hosts() = %v", hosts)
	}
}

// A routes file that is being rewritten, or was hand-edited into nonsense,
// must not take every live deploy down.
func TestBrokenRoutesFileKeepsTheLastGoodTable(t *testing.T) {
	dir := staticDir(t, map[string]string{"index.html": "the app"})
	s, path := serverWith(t, Route{Host: "one.loods.site", Dir: dir})

	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.Refresh()

	if w := get(t, s, "one.loods.site", "/"); w.Code != http.StatusOK {
		t.Fatalf("code = %d, want the deploy to keep serving", w.Code)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deploys.json")
	st := NewStore(path)
	want := []Route{{Host: "a.loods.site", Project: "g/a", Upstream: "127.0.0.1:8900"}}
	if err := st.Write(want); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var table Table
	if err := json.Unmarshal(b, &table); err != nil {
		t.Fatalf("the routes file is not readable JSON: %v", err)
	}
	if len(table.Routes) != 1 || table.Routes[0] != want[0] {
		t.Fatalf("round trip = %+v", table.Routes)
	}
}
