package plan

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestPlanStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plans.yaml")
	ps := NewStore(path)
	st, prio := "active", 1
	tasks := []Task{{Text: "ship it"}, {Text: "write tests", State: "done"}, {Text: "half done", State: "doing"}, {Text: "  "}}
	if _, err := ps.Update("g/p", func(p *Plan) error {
		Patch{Status: &st, Priority: &prio, Tasks: &tasks, Note: "started"}.Apply(p, "you")
		p.Next = "fix\nlogin"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	for _, want := range []string{
		"# loods plans", "- '#1 ship it'", "- '#2 [x] write tests'", "- '#3 [~] half done'",
		"next: fix login", "status: active", "last_task_id: 3",
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("file lacks %q:\n%s", want, b)
		}
	}

	// A second store (the CLI) sees it, and the file reads back the same.
	other := NewStore(path)
	other.Refresh()
	p := other.Get("g/p")
	if p.Status != "active" || p.Priority != 1 || len(p.Tasks) != 3 || !p.Tasks[1].Done() || p.Tasks[0].Done() || p.Tasks[2].State != "doing" {
		t.Fatalf("read back %+v", p)
	}
	if len(p.Log) != 2 || p.Log[0].Text != "status — → active" || p.Log[1].Text != "started" {
		t.Errorf("log = %+v", p.Log)
	}

	bad := "someday"
	if _, err := ps.Update("g/p", func(p *Plan) error { Patch{Status: &bad}.Apply(p, "you"); return nil }); err == nil {
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
	set := func(st string) { Patch{Status: &st}.Apply(p, "you") }
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
	p.AddLog("you", "did a thing")
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
			if _, err := NewStore(path).Update("g/p", func(p *Plan) error {
				p.Tasks = append(p.Tasks, Task{Text: "t" + string(rune('a'+i))})
				return nil
			}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	ps := NewStore(path)
	ps.Refresh()
	if n := len(ps.Get("g/p").Tasks); n != 20 {
		t.Fatalf("%d tasks survived, want 20 (lost updates)", n)
	}
}

func TestFindTask(t *testing.T) {
	// Numbers are the stable task ids, not positions: #7 stays #7 after a move.
	tasks := []Task{{ID: 5, Text: "Write README"}, {ID: 7, Text: "write tests"}, {ID: 2, Text: "ship"}}
	for _, ref := range []string{"7", "#7"} {
		if i, err := FindTask(tasks, ref); err != nil || i != 1 {
			t.Errorf("FindTask(%q) = %d, %v", ref, i, err)
		}
	}
	if i, err := FindTask(tasks, "ship"); err != nil || i != 2 {
		t.Error(i, err)
	}
	if _, err := FindTask(tasks, "write"); err == nil {
		t.Error("ambiguous match accepted")
	}
	if _, err := FindTask(tasks, "3"); err == nil {
		t.Error("a number no task carries was accepted")
	}
	if _, err := FindTask(tasks, "1"); err == nil {
		t.Error("position 1 was taken for a task number")
	}
}

func TestParseTaskBoxes(t *testing.T) {
	for in, want := range map[string]Task{
		"ship it":          {Text: "ship it"},
		"[ ] ship it":      {Text: "ship it"},
		"[x] shipped":      {Text: "shipped", State: "done"},
		"[X] shipped":      {Text: "shipped", State: "done"},
		"[~] half":         {Text: "half", State: "doing"},
		"  [~]half  ":      {Text: "half", State: "doing"},
		"[?] odd box":      {Text: "[?] odd box"},
		"#7 ship it":       {ID: 7, Text: "ship it"},
		"#7 [x] shipped":   {ID: 7, Text: "shipped", State: "done"},
		"#12  [~] half":    {ID: 12, Text: "half", State: "doing"},
		"#0 not a number":  {Text: "#0 not a number"},
		"#x not a number":  {Text: "#x not a number"},
		"# 4 not a number": {Text: "# 4 not a number"},
	} {
		if got := ParseTask(in); got != want {
			t.Errorf("ParseTask(%q) = %+v, want %+v", in, got, want)
		}
	}
}

func TestInvalidTaskStateRejected(t *testing.T) {
	ps := NewStore(filepath.Join(t.TempDir(), "plans.yaml"))
	tasks := []Task{{Text: "x", State: "later"}}
	if _, err := ps.Update("g/p", func(p *Plan) error { Patch{Tasks: &tasks}.Apply(p, "you"); return nil }); err == nil {
		t.Error("invalid task state accepted")
	}
}

// Numbers must survive everything you can do to a task list, because you point
// at a task by its number in a sentence.
func TestTaskNumbersAreStable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plans.yaml")
	ps := NewStore(path)
	set := func(tasks []Task) Plan {
		p, err := ps.Update("g/p", func(p *Plan) error { p.Tasks = tasks; return nil })
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	p := set([]Task{{Text: "one"}, {Text: "two"}, {Text: "three"}})
	if p.Tasks[0].ID != 1 || p.Tasks[2].ID != 3 || p.LastTaskID != 3 {
		t.Fatalf("first numbering: %+v", p.Tasks)
	}

	// Delete the first, reorder the rest, add one: the survivors keep their number
	// and the new task gets a fresh one, never a recycled one.
	p = set([]Task{{ID: 3, Text: "three", State: "done"}, {ID: 2, Text: "two"}, {Text: "four"}})
	if p.Tasks[0].ID != 3 || p.Tasks[1].ID != 2 || p.Tasks[2].ID != 4 || p.LastTaskID != 4 {
		t.Fatalf("after reorder: %+v (last %d)", p.Tasks, p.LastTaskID)
	}

	// A hand-written duplicate number is taken off the later task.
	p = set([]Task{{ID: 2, Text: "two"}, {ID: 2, Text: "copy of two"}})
	if p.Tasks[0].ID != 2 || p.Tasks[1].ID != 5 {
		t.Fatalf("after duplicate: %+v", p.Tasks)
	}

	// A hand-written number above the counter pushes the counter up.
	p = set([]Task{{ID: 40, Text: "far ahead"}, {Text: "next"}})
	if p.Tasks[1].ID != 41 || p.LastTaskID != 41 {
		t.Fatalf("after a high number: %+v (last %d)", p.Tasks, p.LastTaskID)
	}

	// And the file still reads back the same way.
	other := NewStore(path)
	other.Refresh()
	if got := other.Get("g/p"); got.Tasks[0].ID != 40 || got.LastTaskID != 41 {
		t.Fatalf("read back %+v (last %d)", got.Tasks, got.LastTaskID)
	}
}

func TestTaskByIDAndOpenTasks(t *testing.T) {
	p := &Plan{Tasks: []Task{
		{ID: 1, Text: "done one", State: "done"},
		{ID: 2, Text: "busy", State: "doing"},
		{ID: 3, Text: "todo"},
	}}
	if i, ok := p.TaskByID(2); !ok || i != 1 {
		t.Errorf("taskByID(2) = %d, %v", i, ok)
	}
	if _, ok := p.TaskByID(9); ok {
		t.Error("taskByID found a task that is not there")
	}
	open := p.OpenTasks()
	if len(open) != 2 || open[0].ID != 3 || open[1].ID != 2 {
		t.Errorf("openTasks = %+v, want to do first", open)
	}
}
