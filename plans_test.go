package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPlanStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plans.yaml")
	ps := newPlanStore(path)
	st, prio := "active", 1
	tasks := []Task{{Text: "ship it"}, {Text: "write tests", Done: true}, {Text: "  "}}
	if _, err := ps.Update("g/p", func(p *Plan) error {
		PlanPatch{Status: &st, Priority: &prio, Tasks: &tasks, Note: "started"}.apply(p, "you")
		p.Next = "fix\nlogin"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	for _, want := range []string{"# loods plans", "- ship it", "- '[x] write tests'", "next: fix login", "status: active"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("file lacks %q:\n%s", want, b)
		}
	}

	// A second store (the CLI) sees it, and the file reads back the same.
	other := newPlanStore(path)
	other.Refresh()
	p := other.Get("g/p")
	if p.Status != "active" || p.Priority != 1 || len(p.Tasks) != 2 || !p.Tasks[1].Done || p.Tasks[0].Done {
		t.Fatalf("read back %+v", p)
	}
	if len(p.Log) != 2 || p.Log[0].Text != "status — → active" || p.Log[1].Text != "started" {
		t.Errorf("log = %+v", p.Log)
	}

	bad := "someday"
	if _, err := ps.Update("g/p", func(p *Plan) error { PlanPatch{Status: &bad}.apply(p, "you"); return nil }); err == nil {
		t.Error("invalid status accepted")
	}

	// Clearing everything removes the project from the file.
	if _, err := ps.Update("g/p", func(p *Plan) error { *p = Plan{}; return nil }); err != nil {
		t.Fatal(err)
	}
	other.Refresh()
	if all, _ := other.All(); len(all) != 0 {
		t.Errorf("empty plan kept: %+v", all)
	}
}

func TestStatusLogFolds(t *testing.T) {
	p := &Plan{}
	set := func(st string) { PlanPatch{Status: &st}.apply(p, "you") }
	set("active")
	set("paused")
	set("shipped")
	if len(p.Log) != 1 || p.Log[0].Text != "status — → shipped" {
		t.Fatalf("log = %+v", p.Log)
	}
	set("")
	if len(p.Log) != 0 {
		t.Fatalf("round trip should leave no entry: %+v", p.Log)
	}
	p.addLog("you", "did a thing")
	set("idea")
	set("active")
	if len(p.Log) != 2 || p.Log[1].Text != "status — → active" {
		t.Fatalf("log = %+v", p.Log)
	}
}

func TestPlanStoreConcurrentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plans.yaml")
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Separate stores, as the server and several CLIs would be.
			if _, err := newPlanStore(path).Update("g/p", func(p *Plan) error {
				p.Tasks = append(p.Tasks, Task{Text: "t" + string(rune('a'+i))})
				return nil
			}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	ps := newPlanStore(path)
	ps.Refresh()
	if n := len(ps.Get("g/p").Tasks); n != 20 {
		t.Fatalf("%d tasks survived, want 20 (lost updates)", n)
	}
}

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

	ci := newClaudeIndex(dir)
	if !ci.Refresh() {
		t.Fatal("no change on first refresh")
	}
	items := []*Item{{Rel: "g/p", Path: "/home/x/Projects/g/p"}, {Rel: "g", Path: "/home/x/Projects/g"}}
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
	if sum := summarize(s, t0.Add(time.Hour+time.Minute)); !sum.Live || sum.Sessions7d != 1 || sum.LastTitle != "Fix login" {
		t.Errorf("summary = %+v", sum)
	}
}

func TestEditHooksKeepsOtherSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	orig := `{
  "model": "opus",
  "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "notify"}]}]},
  "permissions": {"allow": ["Bash(ls:*)"]}
}
`
	os.WriteFile(path, []byte(orig), 0o640)
	if changed, err := editHooks(path, true); err != nil || !changed {
		t.Fatal(changed, err)
	}
	if changed, _ := editHooks(path, true); changed {
		t.Error("second install changed the file again")
	}
	b, _ := os.ReadFile(path)
	s := string(b)
	if !(strings.Index(s, `"model"`) < strings.Index(s, `"hooks"`) && strings.Index(s, `"hooks"`) < strings.Index(s, `"permissions"`)) {
		t.Errorf("key order changed:\n%s", s)
	}
	if !strings.Contains(s, `"notify"`) || !strings.Contains(s, `loods hook"`) || !strings.Contains(s, "SessionStart") {
		t.Errorf("hooks wrong:\n%s", s)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v", info.Mode().Perm())
	}
	if changed, err := editHooks(path, false); err != nil || !changed {
		t.Fatal(changed, err)
	}
	b, _ = os.ReadFile(path)
	if strings.Contains(string(b), "SessionStart") || !strings.Contains(string(b), `"notify"`) {
		t.Errorf("uninstall left:\n%s", b)
	}
}

func TestFindTask(t *testing.T) {
	tasks := []Task{{Text: "Write README"}, {Text: "write tests"}, {Text: "ship"}}
	if i, err := findTask(tasks, "2"); err != nil || i != 1 {
		t.Error(i, err)
	}
	if i, err := findTask(tasks, "ship"); err != nil || i != 2 {
		t.Error(i, err)
	}
	if _, err := findTask(tasks, "write"); err == nil {
		t.Error("ambiguous match accepted")
	}
	if _, err := findTask(tasks, "9"); err == nil {
		t.Error("out of range accepted")
	}
}

// LOODS_REAL=1 go test -run TestRealTranscripts -v: index ~/.claude for real.
func TestRealTranscripts(t *testing.T) {
	if os.Getenv("LOODS_REAL") == "" {
		t.Skip("set LOODS_REAL=1")
	}
	home, _ := os.UserHomeDir()
	start := time.Now()
	ci := newClaudeIndex(claudeProjectsDir())
	ci.Refresh()
	t.Logf("indexed %d transcripts in %v", len(ci.files), time.Since(start))
	start = time.Now()
	ci.Refresh()
	t.Logf("second refresh %v", time.Since(start))
	items := boardItems(filepath.Join(home, "Projects"), 3, filepath.Join(home, "Archive"))
	now := time.Now()
	for rel, list := range ci.Sessions(items) {
		sum := summarize(list, now)
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

func TestSessionProjectFallsBackToName(t *testing.T) {
	items := []*Item{{Rel: "b/barsys", Name: "barsys", Path: "/nonexistent/P/b/barsys"}, {Rel: "t/kit", Name: "kit", Path: "/nonexistent/P/t/kit"}}
	for dir, want := range map[string]string{
		"/nonexistent/P/barsys":          "b/barsys",
		"/nonexistent/P/t/old/kit/tools": "t/kit",
		"/nonexistent/P/t/workspace":     "",
		"/nonexistent/P/b/barsys/app":    "b/barsys",
	} {
		got := ""
		if it := sessionProject(items, dir); it != nil {
			got = it.Rel
		}
		if got != want {
			t.Errorf("%s → %q, want %q", dir, got, want)
		}
	}
}
