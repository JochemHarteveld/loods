package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"loods/internal/claim"
	"loods/internal/gitx"
	"loods/internal/plan"
	"loods/internal/project"
	"loods/internal/textfmt"
	"loods/internal/xdg"
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

// row is one task as the --json output sees it: the task plus enough of its
// project to decide whether to pick it up.
type row struct {
	Ref      string       `json:"ref"`
	Project  string       `json:"project"`
	Name     string       `json:"name"`
	Task     int          `json:"task"`
	Text     string       `json:"text"`
	State    string       `json:"state"`
	Status   string       `json:"project_status,omitempty"`
	Priority int          `json:"project_priority,omitempty"`
	Next     string       `json:"project_next,omitempty"`
	Claim    *claim.Claim `json:"claim,omitempty"`
}

func newRow(it *project.Item, t plan.Task, p plan.Plan) row {
	return row{
		Ref: fmt.Sprintf("%s#%d", it.Rel, t.ID), Project: it.Rel, Name: it.Name,
		Task: t.ID, Text: t.Text, State: t.State,
		Status: p.Status, Priority: p.Priority, Next: p.Next,
	}
}

func runTodo(env Env, args []string) error {
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
		f.session = first(os.Getenv("LOODS_AGENT_SESSION"), os.Getenv("CLAUDE_SESSION_ID"))
	}

	cmd, rest := "list", words
	if len(words) > 0 {
		cmd, rest = words[0], words[1:]
	}
	plans := plan.NewStore(xdg.PlansPath())
	plans.Refresh()
	claims := claim.NewStore(xdg.ClaimsPath())
	claims.Refresh()

	switch cmd {
	case "list":
		return todoList(env, fs, plans, claims, f)
	case "show", "claim", "heartbeat", "done", "release":
		if len(rest) != 1 {
			fs.Usage()
			return fmt.Errorf("%s needs one task, like loods#4", cmd)
		}
		it, id, err := taskRef(env, rest[0], f.project)
		if err != nil {
			return err
		}
		return todoOne(cmd, it, id, plans, claims, f)
	case "add":
		if len(rest) == 0 {
			fs.Usage()
			return errors.New("add needs the task text")
		}
		it, err := locate(env, f.project)
		if err != nil {
			return err
		}
		return todoAdd(it, strings.Join(rest, " "), plans, f)
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

func todoList(env Env, fs *flag.FlagSet, plans *plan.Store, claims *claim.Store, f todoFlags) error {
	if !slices.Contains([]string{"", "open", "todo", "doing", "done", "any"}, f.state) {
		fs.Usage()
		return fmt.Errorf("unknown --state %q", f.state)
	}
	all, _ := plans.All()
	held := claims.All()

	// Which projects to walk: all of them, or just the one we are in.
	var scope []*project.Item
	if f.all {
		scope = project.Board(env.Root, env.Depth, env.Archive)
	} else {
		it, err := locate(env, f.project)
		if err != nil {
			return err
		}
		scope = []*project.Item{it}
	}
	sortByWorth(scope, all)

	var rows []row
	for _, it := range scope {
		p := all[it.Rel]
		for _, t := range p.Tasks {
			if !wantState(f.state, t.State) {
				continue
			}
			r := newRow(it, t, p)
			if c, ok := held[claim.Key(it.Rel, t.ID)]; ok {
				r.Claim = &c
			}
			if f.mine && !mine(r.Claim, f) {
				continue
			}
			rows = append(rows, r)
		}
	}
	if f.asJSON {
		if rows == nil {
			rows = []row{}
		}
		return writeJSON(os.Stdout, rows)
	}
	printTodos(os.Stdout, rows, f)
	return nil
}

// sortByWorth puts the projects worth doing first: P1 before P3 before none,
// then the most recent work.
func sortByWorth(items []*project.Item, plans map[string]plan.Plan) {
	priority := func(it *project.Item) int {
		if p := plans[it.Rel].Priority; p != 0 {
			return p
		}
		return 9
	}
	sort.SliceStable(items, func(i, j int) bool {
		if pi, pj := priority(items[i]), priority(items[j]); pi != pj {
			return pi < pj
		}
		return items[i].Activity.After(items[j].Activity)
	})
}

func mine(c *claim.Claim, f todoFlags) bool {
	if c == nil {
		return false
	}
	if f.session != "" && c.Session != "" {
		return c.Session == f.session
	}
	return c.Agent == f.agent
}

func printTodos(w io.Writer, rows []row, f todoFlags) {
	if len(rows) == 0 {
		what := "open tasks"
		if f.state != "" && f.state != "open" {
			what = f.state + " tasks"
		}
		fmt.Fprintf(w, "no %s. Add one with `loods todo add \"…\"`\n", what)
		return
	}
	now := time.Now()
	shown := ""
	for _, r := range rows {
		if r.Project != shown {
			shown = r.Project
			fmt.Fprintf(w, "\n%s\n", header(r.Project, r.Status, r.Priority))
			if r.Next != "" {
				fmt.Fprintf(w, "  next: %s\n", r.Next)
			}
		}
		line := fmt.Sprintf("  #%-3d %s %s", r.Task, plan.Task{State: r.State}.Box(), r.Text)
		if c := r.Claim; c != nil {
			state := "working"
			if c.Stale {
				state = "stalled"
			}
			line += fmt.Sprintf("   ← %s %s %s", c.Agent, state, textfmt.Ago(c.Beat, now))
		}
		fmt.Fprintln(w, line)
	}
	fmt.Fprintln(w)
}

func todoOne(cmd string, it *project.Item, id int, plans *plan.Store, claims *claim.Store, f todoFlags) error {
	p := plans.Get(it.Rel)
	i, ok := p.TaskByID(id)
	if !ok {
		return fmt.Errorf("%s has no task #%d (see `loods todo list -p %s --state any`)", it.Rel, id, it.Rel)
	}
	task := p.Tasks[i]

	switch cmd {
	case "show":
		return todoShow(it, task, plans, claims, f)
	case "heartbeat":
		c, err := claims.Heartbeat(it.Rel, id, f.session)
		if err != nil {
			return err
		}
		return todoResult(fmt.Sprintf("%s#%d still with %s\n", it.Rel, id, c.Agent), &c, f)
	case "claim":
		return todoClaim(it, task, plans, claims, f)
	case "done", "release":
		return todoFinish(cmd, it, task, plans, claims, f)
	}
	return fmt.Errorf("unknown todo command %q", cmd)
}

func todoClaim(it *project.Item, task plan.Task, plans *plan.Store, claims *claim.Store, f todoFlags) error {
	branch, _ := gitx.Run(it.Path, "rev-parse", "--abbrev-ref", "HEAD")
	c, err := claims.Take(claim.Claim{
		Project: it.Rel, Task: task.ID, Text: task.Text,
		Agent: f.agent, Session: f.session, Branch: strings.TrimSpace(branch),
	}, f.steal)
	if err != nil {
		return err
	}
	// A claimed task is being worked on, so it belongs in the middle column.
	if task.State != "doing" {
		if _, err := plans.Update(it.Rel, func(p *plan.Plan) error {
			p.SetState(task.ID, "doing")
			return nil
		}); err != nil {
			claims.Release(it.Rel, task.ID)
			return err
		}
	}
	msg := fmt.Sprintf("%s#%d is yours: %s\n  in progress on the loods planboard; heartbeat every few minutes, then `loods todo done %s#%d --note \"…\"`\n",
		it.Rel, task.ID, task.Text, it.Rel, task.ID)
	return todoResult(msg, &c, f)
}

func todoFinish(cmd string, it *project.Item, task plan.Task, plans *plan.Store, claims *claim.Store, f todoFlags) error {
	state, verb := "done", "finished"
	if cmd == "release" {
		state, verb = "", "handed back"
	}
	note := strings.TrimSpace(f.note)
	if _, err := plans.Update(it.Rel, func(p *plan.Plan) error {
		if !p.SetState(task.ID, state) {
			return fmt.Errorf("task #%d is gone", task.ID)
		}
		what := task.Text
		if note != "" {
			what = note
		}
		p.AddLog(f.agent, fmt.Sprintf("#%d %s: %s", task.ID, verb, what))
		return nil
	}); err != nil {
		return err
	}
	claims.Release(it.Rel, task.ID)
	return todoResult(fmt.Sprintf("%s#%d %s: %s\n", it.Rel, task.ID, verb, task.Text), nil, f)
}

func todoShow(it *project.Item, task plan.Task, plans *plan.Store, claims *claim.Store, f todoFlags) error {
	p := plans.Get(it.Rel)
	c, has := claims.Get(it.Rel, task.ID)
	if f.asJSON {
		r := newRow(it, task, p)
		if has {
			r.Claim = &c
		}
		return writeJSON(os.Stdout, r)
	}
	fmt.Printf("%s#%d  %s  %s\n", it.Rel, task.ID, task.Box(), task.Text)
	if has {
		state := "working on it"
		if c.Stale {
			state = "stalled (no heartbeat)"
		}
		fmt.Printf("  claimed by %s, %s, since %s\n", c.Agent, state, textfmt.Ago(c.ClaimedAt, time.Now()))
	}
	fmt.Println()
	printPlan(os.Stdout, it.Rel, p)
	return nil
}

func todoAdd(it *project.Item, text string, plans *plan.Store, f todoFlags) error {
	p, err := plans.Update(it.Rel, func(p *plan.Plan) error {
		p.Tasks = append(p.Tasks, plan.Task{Text: text})
		return nil
	})
	if err != nil {
		return err
	}
	// The number is handed out by the store, so read it back off the task.
	id := 0
	for _, t := range p.Tasks {
		if t.Text == text && t.ID > id {
			id = t.ID
		}
	}
	if f.asJSON {
		return writeJSON(os.Stdout, row{
			Ref: fmt.Sprintf("%s#%d", it.Rel, id), Project: it.Rel, Name: it.Name, Task: id, Text: text,
		})
	}
	fmt.Printf("%s#%d added: %s\n", it.Rel, id, text)
	return nil
}

func todoResult(msg string, c *claim.Claim, f todoFlags) error {
	if f.asJSON {
		return writeJSON(os.Stdout, struct {
			OK    bool         `json:"ok"`
			Note  string       `json:"note"`
			Claim *claim.Claim `json:"claim,omitempty"`
		}{true, strings.TrimSpace(msg), c})
	}
	fmt.Print(msg)
	return nil
}
