package gitx

import "testing"

func TestWebURL(t *testing.T) {
	cases := map[string]string{
		"git@github.com:JochemHarteveld/bierreel.git":     "https://github.com/JochemHarteveld/bierreel",
		"https://github.com/JochemHarteveld/extase.git":   "https://github.com/JochemHarteveld/extase",
		"https://user:token@github.com/Org/repo.git":      "https://github.com/Org/repo",
		"ssh://git@gitlab.example.com:2222/team/proj.git": "https://gitlab.example.com/team/proj",
		"/srv/git/local.git":                              "",
	}
	for in, want := range cases {
		if got := WebURL(in); got != want {
			t.Errorf("WebURL(%q) = %q, want %q", in, got, want)
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
		a, b, g := ParseTrack(c.in)
		if a != c.ahead || b != c.behin || g != c.gone {
			t.Errorf("ParseTrack(%q) = %d,%d,%v", c.in, a, b, g)
		}
	}
}
