package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestWebURL(t *testing.T) {
	cases := map[string]string{
		"git@github.com:JochemHarteveld/bierreel.git":     "https://github.com/JochemHarteveld/bierreel",
		"https://github.com/JochemHarteveld/extase.git":   "https://github.com/JochemHarteveld/extase",
		"https://user:token@github.com/Org/repo.git":      "https://github.com/Org/repo",
		"ssh://git@gitlab.example.com:2222/team/proj.git": "https://gitlab.example.com/team/proj",
		"/srv/git/local.git":                              "",
	}
	for in, want := range cases {
		if got := webURL(in); got != want {
			t.Errorf("webURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseTrack(t *testing.T) {
	cases := []struct {
		in           string
		ahead, behin int
		gone         bool
	}{
		{"", 0, 0, false},
		{"gone", 0, 0, true},
		{"ahead 3", 3, 0, false},
		{"behind 2", 0, 2, false},
		{"ahead 1, behind 4", 1, 4, false},
	}
	for _, c := range cases {
		a, b, g := parseTrack(c.in)
		if a != c.ahead || b != c.behin || g != c.gone {
			t.Errorf("parseTrack(%q) = %d,%d,%v", c.in, a, b, g)
		}
	}
}

func TestGuard(t *testing.T) {
	s := newServer(t.TempDir(), t.TempDir(), 1, 7777, filepath.Join(t.TempDir(), "procs.json"))
	h := s.handler()
	cases := []struct {
		name, method, host, header string
		want                       int
	}{
		{"ping", "GET", "127.0.0.1:7777", "", http.StatusOK},
		{"localhost ok", "GET", "localhost:7777", "", http.StatusOK},
		{"rebinding host", "GET", "evil.example:7777", "", http.StatusForbidden},
		{"post without header", "POST", "127.0.0.1:7777", "", http.StatusForbidden},
		{"post with header", "POST", "127.0.0.1:7777", "1", http.StatusAccepted},
	}
	for _, c := range cases {
		path := "/api/ping"
		if c.method == "POST" {
			path = "/api/rescan"
		}
		req := httptest.NewRequest(c.method, path, nil)
		req.Host = c.host
		if c.header != "" {
			req.Header.Set("X-Loods", c.header)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: got %d, want %d", c.name, rec.Code, c.want)
		}
	}
}
