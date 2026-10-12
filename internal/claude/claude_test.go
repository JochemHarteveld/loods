package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"loods/internal/project"
	"loods/internal/xdg"
)

func TestTranscriptIncremental(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "-home-x-Projects-g-p")
	os.MkdirAll(proj, 0o755)
	path := filepath.Join(proj, "abc.jsonl")
	t0 := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	line := func(v map[string]any) string { b, _ := json.Marshal(v); return string(b) + "\n" }
	user := func(min int, content any) string {
		return line(map[string]any{"type": "user", "cwd": "/home/x/Projects/g/p/sub", "gitBranch": "main",
			"timestamp": t0.Add(time.Duration(min) * time.Minute), "message": map[string]any{"role": "user", "content": content}})
	}
	os.WriteFile(path, []byte(
		user(0, "fix the login bug please")+
			user(1, []map[string]any{{"type": "tool_result", "content": "ok"}})+
			user(2, "<command-name>/compact</command-name>")+
			line(map[string]any{"type": "assistant", "timestamp": t0.Add(4 * time.Minute)})+
			`{"type":"user","cwd":"/home/x/Projects/g/p","timest`), 0o644) // partial line still being written

	ci := NewIndex(dir)
	if !ci.Refresh() {
		t.Fatal("no change on first refresh")
	}
	items := []*project.Item{{Rel: "g/p", Path: "/home/x/Projects/g/p"}, {Rel: "g", Path: "/home/x/Projects/g"}}
	s := ci.Sessions(items)["g/p"]
	if len(s) != 1 || s[0].Prompts != 1 || s[0].Title != "fix the login bug please" || s[0].ActiveMins != 4 || s[0].Branch != "main" {
		t.Fatalf("sessions = %+v", s)
	}

	// The rest of the partial line plus a title arrive later.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`amp":"2026-10-01T11:00:00Z","message":{"content":"and add a test"}}` + "\n" +
		line(map[string]any{"type": "ai-title", "aiTitle": "Fix login"}))
	f.Close()
	if !ci.Refresh() || ci.Refresh() {
		t.Fatal("refresh should report the append once")
	}
	s = ci.Sessions(items)["g/p"]
	if s[0].Prompts != 2 || s[0].Title != "Fix login" || s[0].ActiveMins != 4 || !s[0].End.Equal(t0.Add(time.Hour)) {
		t.Fatalf("after append = %+v", s[0])
	}
	if sum := Summarize(s, t0.Add(time.Hour+time.Minute)); !sum.Live || sum.Sessions7d != 1 || sum.LastTitle != "Fix login" {
		t.Errorf("summary = %+v", sum)
	}
}

// LOODS_REAL=1 go test -run TestRealTranscripts -v: index ~/.claude for real.
func TestRealTranscripts(t *testing.T) {
	if os.Getenv("LOODS_REAL") == "" {
		t.Skip("set LOODS_REAL=1")
	}
	home, _ := os.UserHomeDir()
	start := time.Now()
	ci := NewIndex(xdg.ClaudeProjectsDir())
	ci.Refresh()
	t.Logf("indexed %d transcripts in %v", len(ci.files), time.Since(start))
	start = time.Now()
	ci.Refresh()
	t.Logf("second refresh %v", time.Since(start))
	items := project.Board(filepath.Join(home, "Projects"), 3, filepath.Join(home, "Archive"))
	now := time.Now()
	for rel, list := range ci.Sessions(items) {
		sum := Summarize(list, now)
		t.Logf("%-45s %3d sessions  last %s  %q  (7d: %d, %d min)", rel, len(list), sum.LastAt.Local().Format("01-02 15:04"), sum.LastTitle, sum.Sessions7d, sum.Mins7d)
	}
}

func TestPromptText(t *testing.T) {
	cases := map[string]string{
		`"fix it"`: "fix it",
		`[{"type":"text","text":"<ide_opened_file>x</ide_opened_file>"},{"type":"text","text":"hi"}]`:                              "hi",
		`"<command-message>wayfinder</command-message>\n<command-name>/wayfinder</command-name>\n<command-args>go</command-args>"`: "/wayfinder go",
		`"<command-name>/compact</command-name>"`:             "",
		`[{"type":"tool_result","content":"ok"}]`:             "",
		`"<local-command-stdout>done</local-command-stdout>"`: "",
	}
	for in, want := range cases {
		got, ok := promptText(json.RawMessage(in))
		if got != want || ok != (want != "") {
			t.Errorf("promptText(%s) = %q, %v; want %q", in, got, ok, want)
		}
	}
}
