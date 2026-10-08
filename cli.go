package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"maps"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// `loods plan …`: read and edit the plan of the project you are in. Claude
// uses it from /wrapup; it works with or without the server running.

const planUsage = `usage: loods plan [-p group/project] [command]

  (none)              show the plan
  next <text>         set the next step
  status <status>     idea | active | paused | shipped | dead | none
  priority <0-3>      1 high … 3 low, 0 none
  note <text>         add a log entry
  task <text>         add a task (to do)
  doing <n|text>      move a task to in progress (number from ` + "`loods plan`" + `)
  done <n|text>       tick a task
  undo <n|text>       move a task back to to do
  drop <n|text>       delete a task

The project is the one containing the current directory, unless -p is given.
`

func runPlan(args []string, root, archive string, depth int) error {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	project := fs.String("p", "", "project path relative to the root")
	fs.Usage = func() { fmt.Fprint(os.Stderr, planUsage) }
	if err := fs.Parse(args); errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return err
	}
	it, err := cliProject(*project, root, archive, depth)
	if err != nil {
		return err
	}
	ps := newPlanStore(plansPath())
	ps.Refresh()
	by := "you"
	if os.Getenv("CLAUDECODE") != "" {
		by = "claude"
	}
	cmd, rest := fs.Arg(0), strings.TrimSpace(strings.Join(fs.Args()[min(1, fs.NArg()):], " "))
	need := func() error {
		if rest == "" {
			return fmt.Errorf("loods plan %s: missing argument", cmd)
		}
		return nil
	}

	var fn func(*Plan) error
	switch cmd {
	case "", "show":
		p := ps.Get(it.Rel)
		printPlan(os.Stdout, it.Rel, p)
		return nil
	case "next":
		fn = func(p *Plan) error { p.Next = rest; return nil }
	case "status":
		if err := need(); err != nil {
			return err
		}
		st := rest
		if st == "none" {
			st = ""
		}
		fn = func(p *Plan) error { PlanPatch{Status: &st}.apply(p, by); return nil }
	case "priority":
		n, err := strconv.Atoi(rest)
		if err != nil {
			return errors.New("priority must be a number 0-3")
		}
		fn = func(p *Plan) error { p.Priority = n; return nil }
	case "note":
		if err := need(); err != nil {
			return err
		}
		fn = func(p *Plan) error { p.addLog(by, rest); return nil }
	case "task":
		if err := need(); err != nil {
			return err
		}
		fn = func(p *Plan) error { p.Tasks = append(p.Tasks, Task{Text: rest}); return nil }
	case "doing", "done", "undo", "drop":
		if err := need(); err != nil {
			return err
		}
		fn = func(p *Plan) error {
			i, err := findTask(p.Tasks, rest)
			if err != nil {
				return err
			}
			switch cmd {
			case "doing":
				p.Tasks[i].State = "doing"
			case "done":
				p.Tasks[i].State = "done"
			case "undo":
				p.Tasks[i].State = ""
			default:
				p.Tasks = slices.Delete(p.Tasks, i, i+1)
			}
			return nil
		}
	default:
		fs.Usage()
		return fmt.Errorf("unknown plan command %q", cmd)
	}
	if _, err := ps.Update(it.Rel, fn); err != nil {
		return err
	}
	fmt.Printf("%s: %s ok\n", it.Rel, cmd)
	return nil
}

// findTask takes the stable task number shown by `loods plan` and the board
// ("#4"), or a piece of the task text. Numbers are not positions: they stay with
// a task while it moves between columns.
func findTask(tasks []Task, ref string) (int, error) {
	if n, err := strconv.Atoi(strings.TrimPrefix(ref, "#")); err == nil {
		for i, t := range tasks {
			if t.ID == n {
				return i, nil
			}
		}
		return 0, fmt.Errorf("no task #%d here (the numbers are the ones `loods plan` prints)", n)
	}
	found := -1
	for i, t := range tasks {
		if strings.Contains(strings.ToLower(t.Text), strings.ToLower(ref)) {
			if found >= 0 {
				return 0, fmt.Errorf("%q matches several tasks; use its number", ref)
			}
			found = i
		}
	}
	if found < 0 {
		return 0, fmt.Errorf("no task matches %q", ref)
	}
	return found, nil
}

func printPlan(w io.Writer, rel string, p Plan) {
	head := []string{rel}
	if p.Status != "" {
		head = append(head, p.Status)
	}
	if p.Priority > 0 {
		head = append(head, fmt.Sprintf("P%d", p.Priority))
	}
	fmt.Fprintln(w, strings.Join(head, "  ·  "))
	if p.empty() {
		fmt.Fprintln(w, "no plan yet: try `loods plan next \"…\"`")
		return
	}
	if p.Next != "" {
		fmt.Fprintf(w, "next:   %s\n", p.Next)
	}
	if len(p.Tasks) > 0 {
		fmt.Fprintln(w, "tasks:")
		for _, t := range p.Tasks {
			box := map[string]string{"": "[ ]", "doing": "[~]", "done": "[x]"}[t.State]
			fmt.Fprintf(w, "  #%-3d %s %s\n", t.ID, box, t.Text)
		}
	}
	if p.Notes != "" {
		fmt.Fprintln(w, "notes:")
		for _, l := range strings.Split(strings.TrimRight(p.Notes, "\n"), "\n") {
			fmt.Fprintln(w, "  "+l)
		}
	}
	if len(p.Log) > 0 {
		fmt.Fprintln(w, "log:")
		for _, e := range p.Log[max(0, len(p.Log)-5):] {
			fmt.Fprintf(w, "  %s  %-6s %s\n", e.At.Local().Format("2006-01-02 15:04"), e.By, e.Text)
		}
	}
}

// cliProject resolves -p, or the project containing the working directory.
func cliProject(rel, root, archive string, depth int) (*Item, error) {
	items := boardItems(root, depth, archive)
	if rel != "" {
		rel = strings.Trim(rel, "/")
		for _, it := range items {
			if it.Rel == rel {
				return it, nil
			}
		}
		return nil, fmt.Errorf("no project %q under %s", rel, root)
	}
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	if it := projectForDir(items, wd); it != nil {
		return it, nil
	}
	return nil, fmt.Errorf("%s is not inside a project under %s (use -p group/project)", wd, root)
}

func boardItems(root string, depth int, archive string) []*Item {
	var out []*Item
	for _, it := range Discover(root, depth, archive) {
		if it.Kind == KindGit || it.Kind == KindProj {
			out = append(out, it)
		}
	}
	return out
}

// projectForDir also follows a linked git worktree outside the project
// folder back to its main repository.
func projectForDir(items []*Item, dir string) *Item {
	if it := projectFor(items, dir); it != nil {
		return it
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return nil
	}
	return projectFor(items, filepath.Dir(strings.TrimSpace(string(out))))
}

// runHook is a Claude Code SessionStart hook: what it prints becomes context
// for the new session, so Claude starts knowing where the project stands.
// It never fails the session.
func runHook(root, archive string, depth int) {
	var in struct {
		Cwd string `json:"cwd"`
	}
	if json.NewDecoder(io.LimitReader(os.Stdin, 1<<20)).Decode(&in) != nil || in.Cwd == "" {
		return
	}
	it := projectForDir(boardItems(root, depth, archive), in.Cwd)
	if it == nil {
		return
	}
	ps := newPlanStore(plansPath())
	ps.Refresh()
	p := ps.Get(it.Rel)
	if p.empty() {
		fmt.Printf("This project (%s) is on the loods board without a plan yet. When the user wraps up, /wrapup records its status, next step and tasks.\n", it.Rel)
		return
	}
	fmt.Println("Where this project stands, from the loods board (`loods plan`):")
	fmt.Println()
	var b bytes.Buffer
	printPlan(&b, it.Rel, p)
	fmt.Print(b.String())
	fmt.Println()

	// Task numbers are stable, so the user can say "do todo #4" and mean it.
	cs := newClaimStore(claimsPath())
	cs.Refresh()
	if open := p.openTasks(); len(open) > 0 {
		fmt.Printf("Open tasks you can be asked to pick up, by number (%s#<n>):\n", it.Rel)
		for _, t := range open {
			line := fmt.Sprintf("  #%-3d %s", t.ID, t.Text)
			if t.State == "doing" {
				line += "  (in progress)"
			}
			if c, ok := cs.Get(it.Rel, t.ID); ok {
				state := "working on it"
				if c.Stale {
					state = "stalled, no heartbeat"
				}
				line += fmt.Sprintf("  ← %s %s", c.Agent, state)
			}
			fmt.Println(line)
		}
		fmt.Println()
		fmt.Println("Asked to do one? Claim it first: `loods todo claim " + it.Rel + "#<n>` (see /todo).")
	}
	fmt.Println("Keep it current: when the user wraps up, run /wrapup (or `loods plan next|note|task|doing|done`).")
}

// --- loods claude install / uninstall ---

const skillMarker = "<!-- written by `loods claude install` -->"

const wrapupSkill = `---
name: wrapup
description: Record where this project stands on the loods board — what this session did, the next step, open tasks and status. Use when the user wraps up a session or asks to update the project plan.
allowed-tools: Bash(loods plan:*)
---
` + skillMarker + `

Update this project's plan on the loods board, so the next session (and the
user, looking at the board) knows where things stand.

1. Run ` + "`loods plan`" + ` to see the current plan.
2. Log what this session did in one or two concrete sentences: what changed,
   what is verified, what is not: ` + "`loods plan note \"…\"`" + `
3. Set the single most useful next step, specific enough to start on without
   rereading this session: ` + "`loods plan next \"…\"`" + `
4. Move tasks to the column they are in now: ` + "`loods plan done <n>`" + ` for
   finished ones, ` + "`loods plan doing <n>`" + ` for what is half-built, and add
   real, actionable follow-ups (` + "`loods plan task \"…\"`" + `). Skip if none.
5. Change the status only if it clearly changed
   (` + "`loods plan status idea|active|paused|shipped|dead`" + `). Leave priority
   to the user.

Then tell the user in two or three lines what you recorded.

Extra instructions from the user, if any: $ARGUMENTS
`

const todoSkill = `---
name: todo
description: Pick up, work on and finish numbered tasks from the loods planboard. Use when the user says "do todo #4 of <project>", asks which tasks are open or what to pick up, or hands you work from the board.
allowed-tools: Bash(loods todo:*), Bash(loods plan:*)
---
` + skillMarker + `

Work the user hands you from the loods planboard. Every task has a number that
stays with it, so "#4 of loods" always means the same task.

1. **Find the work.** ` + "`loods todo list`" + ` for this project,
   ` + "`loods todo list --all`" + ` for every project (highest priority first,
   with each project's status and next step). Add ` + "`--json`" + ` when you want
   to pick from it programmatically. The user often names one outright: "do todo
   #4 of loods".
2. **Claim it before you touch anything:** ` + "`loods todo claim <project>#<n>`" + `.
   That moves the task to in progress and shows on the board that you are on it,
   so the user can see what is being worked on and does not start it themselves.
   If another agent is actively on it the claim is refused: say so and stop. Only
   pass ` + "`--steal`" + ` when the user explicitly tells you to take it over.
3. **Stay visible.** Run ` + "`loods todo heartbeat <project>#<n>`" + ` as you go:
   after finishing a step, and before anything long. Without a heartbeat for ten
   minutes the board marks the task as stalled, which is how the user spots an
   agent that died.
4. **Finish it:** ` + "`loods todo done <project>#<n> --note \"what you did\"`" + `.
   The note lands in the project's log, so write what changed and what is
   verified, not "done".
5. **Or hand it back:** ` + "`loods todo release <project>#<n> --note \"why\"`" + `
   when you stop, get blocked, or the user changes direction. Never walk away from
   a claim: a task that looks claimed but is not being worked on is worse than an
   untouched one.
6. New work you discover while doing this belongs on the board too:
   ` + "`loods todo add \"…\"`" + ` (it prints the number). Do not quietly widen the
   task you claimed.

Tell the user which task you claimed, and at the end what you recorded.

Extra instructions from the user, if any: $ARGUMENTS
`

// skills are everything `loods claude install` writes into ~/.claude/skills.
var skills = map[string]string{"wrapup": wrapupSkill, "todo": todoSkill}

func claudeDir() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

func runClaude(args []string) error {
	if len(args) != 1 || (args[0] != "install" && args[0] != "uninstall") {
		return errors.New("usage: loods claude install|uninstall\n\n" +
			"install adds the /wrapup and /todo skills and a SessionStart hook that shows Claude the plan")
	}
	settings := filepath.Join(claudeDir(), "settings.json")
	names := slices.Sorted(maps.Keys(skills))
	if args[0] == "uninstall" {
		for _, name := range names {
			skill := skillPath(name)
			if b, err := os.ReadFile(skill); err == nil && bytes.Contains(b, []byte(skillMarker)) {
				os.Remove(skill)
				os.Remove(filepath.Dir(skill))
				fmt.Println("removed", skill)
			}
		}
		changed, err := editHooks(settings, false)
		if err == nil && changed {
			fmt.Println("removed the SessionStart hook from", settings)
		}
		return err
	}

	for _, name := range names {
		skill := skillPath(name)
		if b, err := os.ReadFile(skill); err == nil && !bytes.Contains(b, []byte(skillMarker)) {
			return fmt.Errorf("%s exists and was not written by loods; not touching it", skill)
		}
	}
	for _, name := range names {
		skill := skillPath(name)
		if err := os.MkdirAll(filepath.Dir(skill), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(skill, []byte(skills[name]), 0o644); err != nil {
			return err
		}
		fmt.Println("wrote", skill)
	}
	changed, err := editHooks(settings, true)
	if err != nil {
		return err
	}
	if changed {
		fmt.Printf("added a SessionStart hook (%s) to %s\n  backup: %s.loods-backup\n", hookCommand(), settings, settings)
	} else {
		fmt.Println("SessionStart hook already in", settings)
	}
	return nil
}

func skillPath(name string) string {
	return filepath.Join(claudeDir(), "skills", name, "SKILL.md")
}

func hookCommand() string {
	exe := "loods"
	if p, err := exec.LookPath("loods"); err == nil {
		exe = p
	} else if p, err := os.Executable(); err == nil {
		exe = p
	}
	return exe + " hook"
}

func isLoodsHook(e json.RawMessage) bool {
	var h struct {
		Hooks []struct {
			Command string `json:"command"`
		} `json:"hooks"`
	}
	if json.Unmarshal(e, &h) != nil {
		return false
	}
	for _, c := range h.Hooks {
		if strings.HasSuffix(c.Command, "loods hook") {
			return true
		}
	}
	return false
}

// editHooks adds or removes our SessionStart hook in settings.json. Only the
// "hooks" value is rebuilt; every other key keeps its place and formatting.
func editHooks(path string, add bool) (bool, error) {
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	orig, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if !add {
			return false, nil
		}
		orig = []byte("{}")
	} else if err != nil {
		return false, err
	}
	keys, vals, err := topLevel(orig)
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	hooks := map[string][]json.RawMessage{}
	i := slices.Index(keys, "hooks")
	if i >= 0 {
		if err := json.Unmarshal(vals[i], &hooks); err != nil {
			return false, fmt.Errorf("%s: hooks: %w", path, err)
		}
	}
	start := slices.DeleteFunc(slices.Clone(hooks["SessionStart"]), isLoodsHook)
	had := len(start) != len(hooks["SessionStart"])
	if add == had {
		return false, nil
	}
	if add {
		entry, _ := json.Marshal(map[string]any{"hooks": []map[string]any{{"type": "command", "command": hookCommand(), "timeout": 10}}})
		start = append(start, entry)
	}
	if len(start) > 0 {
		hooks["SessionStart"] = start
	} else {
		delete(hooks, "SessionStart")
	}
	hv, _ := json.Marshal(hooks)
	switch {
	case i >= 0 && len(hooks) == 0:
		keys, vals = slices.Delete(keys, i, i+1), slices.Delete(vals, i, i+1)
	case i >= 0:
		vals[i] = hv
	default:
		keys, vals = append(keys, "hooks"), append(vals, hv)
	}
	var out bytes.Buffer
	out.WriteString("{")
	for j, k := range keys {
		if j > 0 {
			out.WriteString(",")
		}
		kb, _ := json.Marshal(k)
		out.Write(kb)
		out.WriteString(":")
		out.Write(vals[j])
	}
	out.WriteString("}")
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, out.Bytes(), "", "  "); err != nil {
		return false, err
	}
	pretty.WriteString("\n")
	if err := os.WriteFile(path+".loods-backup", orig, 0o600); err != nil {
		return false, err
	}
	tmp := path + ".loods-tmp"
	if err := os.WriteFile(tmp, pretty.Bytes(), mode); err != nil {
		return false, err
	}
	return true, os.Rename(tmp, path)
}

// topLevel splits a JSON object into its keys and raw values, in order.
func topLevel(b []byte) ([]string, []json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, nil, errors.New("not a JSON object")
	}
	var keys []string
	var vals []json.RawMessage
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, nil, err
		}
		keys, vals = append(keys, t.(string)), append(vals, v)
	}
	return keys, vals, nil
}
