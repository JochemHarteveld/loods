package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// `loods todo …`: hand work to an agent and get it back. Tasks are addressed by
// the number the planboard shows ("loods#4"), so you can say "do todo #4 of
// loods" and Claude can claim it, work, and report back. Everything goes through
// the same flocked stores as the board, so nothing is lost either way.

const todoUsage = `usage: loods todo [-p group/project] [command] [flags]

  list                open tasks of this project (--all: every project)
  show <ref>          one task with its project's plan around it
  claim <ref>         take it: moves it to in progress and marks it as yours
  heartbeat <ref>     say you are still working on it (every few minutes)
  done <ref>          finish it: moves it to done and drops the claim
  release <ref>       give it back: moves it to to do and drops the claim
  add "<text>"        add a task and print its number

A <ref> is "group/project#4", "#4" or "4"; a bare number means this project
(or the one given with -p). Flags:

  --all               every project, not just this one (list)
  --state <s>         filter: open (default for list), todo, doing, done, any
  --mine              only tasks claimed by this agent or session
  --agent <name>      who is claiming (default claude)
  --session <id>      session id, so the same agent can reclaim without --steal
  --steal             take a task another agent is actively on
  --note "<text>"     log line to write on done or release
  --json              machine-readable output

The project is the one containing the current directory, unless -p or a ref says
otherwise.
`

type todoFlags struct {
	project string
	all     bool
	state   string
	mine    bool
	agent   string
	session string
	steal   bool
	note    string
	asJSON  bool
}

// todoRow is one task as the --json output sees it: the task plus enough of its
// project to decide whether to pick it up.
type todoRow struct {
	Ref      string `json:"ref"`
	Project  string `json:"project"`
	Name     string `json:"name"`
	Task     int    `json:"task"`
	Text     string `json:"text"`
	State    string `json:"state"`
	Status   string `json:"project_status,omitempty"`
	Priority int    `json:"project_priority,omitempty"`
	Next     string `json:"project_next,omitempty"`
	Claim    *Claim `json:"claim,omitempty"`
}

func runTodo(args []string, root, archive string, depth int) error {
	var f todoFlags
	fs := flag.NewFlagSet("todo", flag.ContinueOnError)
	fs.StringVar(&f.project, "p", "", "project path relative to the root")
	fs.BoolVar(&f.all, "all", false, "every project, not just this one")
	fs.StringVar(&f.state, "state", "", "open | todo | doing | done | any")
	fs.BoolVar(&f.mine, "mine", false, "only tasks claimed by this agent or session")
	fs.StringVar(&f.agent, "agent", "", "who is claiming (default claude)")
	fs.StringVar(&f.session, "session", "", "session id of the agent")
	fs.BoolVar(&f.steal, "steal", false, "take a task another agent is actively on")
	fs.StringVar(&f.note, "note", "", "log line to write on done or release")
	fs.BoolVar(&f.asJSON, "json", false, "machine-readable output")
	fs.Usage = func() { fmt.Fprint(os.Stderr, todoUsage) }

	// Flags may come before or after the command and its ref, because that is how
	// anyone (and any agent) types it: `loods todo done loods#4 --note "…"`.
	words, err := parseInterleaved(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return err
	}
	if f.agent == "" {
		f.agent = "claude"
	}
	if f.session == "" {
		f.session = cmp(os.Getenv("LOODS_AGENT_SESSION"), os.Getenv("CLAUDE_SESSION_ID"))
	}

	cmd, rest := "list", words
	if len(words) > 0 {
		cmd, rest = words[0], words[1:]
	}
	ps := newPlanStore(plansPath())
	ps.Refresh()
	cs := newClaimStore(claimsPath())
	cs.Refresh()
	items := boardItems(root, depth, archive)

	switch cmd {
	case "list":
		return todoList(fs, items, ps, cs, f, root, archive, depth)
	case "show", "claim", "heartbeat", "done", "release":
		if len(rest) != 1 {
			fs.Usage()
			return fmt.Errorf("%s needs one task, like loods#4", cmd)
		}
		it, id, err := resolveRef(rest[0], f.project, items, root, archive, depth)
		if err != nil {
			return err
		}
		return todoOne(cmd, it, id, ps, cs, f)
	case "add":
		if len(rest) == 0 {
			fs.Usage()
			return errors.New("add needs the task text")
		}
		it, err := cliProject(f.project, root, archive, depth)
		if err != nil {
			return err
		}
		return todoAdd(it, strings.Join(rest, " "), ps, f)
	default:
		fs.Usage()
		return fmt.Errorf("unknown todo command %q", cmd)
	}
}

// parseInterleaved parses flags that sit anywhere between the positional words.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var words []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return words, nil
		}
		words = append(words, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func cmp(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// resolveRef reads "group/project#4", "#4" or "4".
func resolveRef(ref, pflag string, items []*Item, root, archive string, depth int) (*Item, int, error) {
	rel, num := "", ref
	if i := strings.LastIndex(ref, "#"); i >= 0 {
		rel, num = strings.Trim(ref[:i], "/"), ref[i+1:]
	}
	id, err := strconv.Atoi(strings.TrimSpace(num))
	if err != nil || id <= 0 {
		return nil, 0, fmt.Errorf("%q is not a task number; use loods#4, #4 or 4", ref)
	}
	if rel == "" {
		rel = pflag
	}
	it, err := cliProject(rel, root, archive, depth)
	if err != nil {
		return nil, 0, err
	}
	return it, id, nil
}

// wantState says whether a task passes the --state filter.
func wantState(filter, state string) bool {
	switch filter {
	case "", "open":
		return state != "done"
	case "todo":
		return state == ""
	case "doing", "done":
		return state == filter
	case "any":
		return true
	}
	return false
}

func todoList(fs *flag.FlagSet, items []*Item, ps *PlanStore, cs *ClaimStore, f todoFlags, root, archive string, depth int) error {
	if !slices.Contains([]string{"", "open", "todo", "doing", "done", "any"}, f.state) {
		fs.Usage()
		return fmt.Errorf("unknown --state %q", f.state)
	}
	plans, _ := ps.All()
	claims := cs.All()

	// Which projects to walk: all of them, or just the one we are in.
	var scope []*Item
	if f.all {
		scope = items
	} else {
		it, err := cliProject(f.project, root, archive, depth)
		if err != nil {
			return err
		}
		scope = []*Item{it}
	}
	// Worth doing first: P1 before P3 before none, then the most recent work.
	sort.SliceStable(scope, func(i, j int) bool {
		a, b := plans[scope[i].Rel], plans[scope[j].Rel]
		pa, pb := a.Priority, b.Priority
		if pa == 0 {
			pa = 9
		}
		if pb == 0 {
			pb = 9
		}
		if pa != pb {
			return pa < pb
		}
		return scope[i].Activity.After(scope[j].Activity)
	})

	var rows []todoRow
	for _, it := range scope {
		p := plans[it.Rel]
		for _, t := range p.Tasks {
			if !wantState(f.state, t.State) {
				continue
			}
			row := todoRow{
				Ref: fmt.Sprintf("%s#%d", it.Rel, t.ID), Project: it.Rel, Name: it.Name,
				Task: t.ID, Text: t.Text, State: t.State,
				Status: p.Status, Priority: p.Priority, Next: p.Next,
			}
			if c, ok := claims[claimKey(it.Rel, t.ID)]; ok {
				row.Claim = &c
			}
			if f.mine && !mine(row.Claim, f) {
				continue
			}
			rows = append(rows, row)
		}
	}
	if f.asJSON {
		if rows == nil {
			rows = []todoRow{}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}
	printTodos(os.Stdout, rows, f)
	return nil
}

func mine(c *Claim, f todoFlags) bool {
	if c == nil {
		return false
	}
	if f.session != "" && c.Session != "" {
		return c.Session == f.session
	}
	return c.Agent == f.agent
}

func printTodos(w *os.File, rows []todoRow, f todoFlags) {
	if len(rows) == 0 {
		what := "open tasks"
		if f.state != "" && f.state != "open" {
			what = f.state + " tasks"
		}
		fmt.Fprintf(w, "no %s. Add one with `loods todo add \"…\"`\n", what)
		return
	}
	now := time.Now()
	project := ""
	for _, r := range rows {
		if r.Project != project {
			project = r.Project
			head := []string{r.Project}
			if r.Status != "" {
				head = append(head, r.Status)
			}
			if r.Priority > 0 {
				head = append(head, fmt.Sprintf("P%d", r.Priority))
			}
			fmt.Fprintf(w, "\n%s\n", strings.Join(head, "  ·  "))
			if r.Next != "" {
				fmt.Fprintf(w, "  next: %s\n", r.Next)
			}
		}
		box := map[string]string{"": "[ ]", "doing": "[~]", "done": "[x]"}[r.State]
		line := fmt.Sprintf("  #%-3d %s %s", r.Task, box, r.Text)
		if c := r.Claim; c != nil {
			state := "working"
			if c.Stale {
				state = "stalled"
			}
			line += fmt.Sprintf("   ← %s %s %s", c.Agent, state, ago(c.Beat, now))
		}
		fmt.Fprintln(w, line)
	}
	fmt.Fprintln(w)
}

func todoOne(cmd string, it *Item, id int, ps *PlanStore, cs *ClaimStore, f todoFlags) error {
	p := ps.Get(it.Rel)
	i, ok := p.taskByID(id)
	if !ok {
		return fmt.Errorf("%s has no task #%d (see `loods todo list -p %s --state any`)", it.Rel, id, it.Rel)
	}
	task := p.Tasks[i]

	switch cmd {
	case "show":
		return todoShow(it, task, ps, cs, f)
	case "heartbeat":
		c, err := cs.Heartbeat(it.Rel, id, f.session)
		if err != nil {
			return err
		}
		return todoResult(fmt.Sprintf("%s#%d still with %s\n", it.Rel, id, c.Agent), &c, f)
	case "claim":
		branch, _ := git(it.Path, "rev-parse", "--abbrev-ref", "HEAD")
		c, err := cs.Take(Claim{
			Project: it.Rel, Task: id, Text: task.Text,
			Agent: f.agent, Session: f.session, Branch: strings.TrimSpace(branch),
		}, f.steal)
		if err != nil {
			return err
		}
		// A claimed task is being worked on, so it belongs in the middle column.
		if task.State != "doing" {
			if _, err := ps.Update(it.Rel, func(p *Plan) error {
				if j, ok := p.taskByID(id); ok {
					p.Tasks[j].State = "doing"
				}
				return nil
			}); err != nil {
				cs.Release(it.Rel, id)
				return err
			}
		}
		msg := fmt.Sprintf("%s#%d is yours: %s\n  in progress on the loods planboard; heartbeat every few minutes, then `loods todo done %s#%d --note \"…\"`\n",
			it.Rel, id, task.Text, it.Rel, id)
		return todoResult(msg, &c, f)
	case "done", "release":
		state, verb := "done", "finished"
		if cmd == "release" {
			state, verb = "", "handed back"
		}
		note := strings.TrimSpace(f.note)
		if _, err := ps.Update(it.Rel, func(p *Plan) error {
			j, ok := p.taskByID(id)
			if !ok {
				return fmt.Errorf("task #%d is gone", id)
			}
			p.Tasks[j].State = state
			line := fmt.Sprintf("#%d %s: %s", id, verb, task.Text)
			if note != "" {
				line = fmt.Sprintf("#%d %s: %s", id, verb, note)
			}
			p.addLog(f.agent, line)
			return nil
		}); err != nil {
			return err
		}
		cs.Release(it.Rel, id)
		return todoResult(fmt.Sprintf("%s#%d %s: %s\n", it.Rel, id, verb, task.Text), nil, f)
	}
	return fmt.Errorf("unknown todo command %q", cmd)
}

func todoShow(it *Item, task Task, ps *PlanStore, cs *ClaimStore, f todoFlags) error {
	p := ps.Get(it.Rel)
	c, has := cs.Get(it.Rel, task.ID)
	if f.asJSON {
		row := todoRow{
			Ref: fmt.Sprintf("%s#%d", it.Rel, task.ID), Project: it.Rel, Name: it.Name,
			Task: task.ID, Text: task.Text, State: task.State,
			Status: p.Status, Priority: p.Priority, Next: p.Next,
		}
		if has {
			row.Claim = &c
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(row)
	}
	fmt.Printf("%s#%d  %s  %s\n", it.Rel, task.ID, map[string]string{"": "[ ]", "doing": "[~]", "done": "[x]"}[task.State], task.Text)
	if has {
		state := "working on it"
		if c.Stale {
			state = "stalled (no heartbeat)"
		}
		fmt.Printf("  claimed by %s, %s, since %s\n", c.Agent, state, ago(c.ClaimedAt, time.Now()))
	}
	fmt.Println()
	printPlan(os.Stdout, it.Rel, p)
	return nil
}

func todoAdd(it *Item, text string, ps *PlanStore, f todoFlags) error {
	p, err := ps.Update(it.Rel, func(p *Plan) error {
		p.Tasks = append(p.Tasks, Task{Text: text})
		return nil
	})
	if err != nil {
		return err
	}
	id := 0
	for _, t := range p.Tasks {
		if t.Text == text && t.ID > id {
			id = t.ID
		}
	}
	if f.asJSON {
		return json.NewEncoder(os.Stdout).Encode(todoRow{
			Ref: fmt.Sprintf("%s#%d", it.Rel, id), Project: it.Rel, Name: it.Name, Task: id, Text: text,
		})
	}
	fmt.Printf("%s#%d added: %s\n", it.Rel, id, text)
	return nil
}

func todoResult(msg string, c *Claim, f todoFlags) error {
	if f.asJSON {
		return json.NewEncoder(os.Stdout).Encode(struct {
			OK    bool   `json:"ok"`
			Note  string `json:"note"`
			Claim *Claim `json:"claim,omitempty"`
		}{true, strings.TrimSpace(msg), c})
	}
	fmt.Print(msg)
	return nil
}
