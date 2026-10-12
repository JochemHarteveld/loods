package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"

	"loods/internal/plan"
	"loods/internal/xdg"
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

func runPlan(env Env, args []string) error {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	pflag := fs.String("p", "", "project path relative to the root")
	fs.Usage = func() { fmt.Fprint(os.Stderr, planUsage) }
	if err := fs.Parse(args); errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return err
	}
	it, err := locate(env, *pflag)
	if err != nil {
		return err
	}
	plans := plan.NewStore(xdg.PlansPath())
	plans.Refresh()
	by := author()
	cmd, rest := fs.Arg(0), strings.TrimSpace(strings.Join(fs.Args()[min(1, fs.NArg()):], " "))
	need := func() error {
		if rest == "" {
			return fmt.Errorf("loods plan %s: missing argument", cmd)
		}
		return nil
	}

	var fn func(*plan.Plan) error
	switch cmd {
	case "", "show":
		printPlan(os.Stdout, it.Rel, plans.Get(it.Rel))
		return nil
	case "next":
		fn = func(p *plan.Plan) error { p.Next = rest; return nil }
	case "status":
		if err := need(); err != nil {
			return err
		}
		st := rest
		if st == "none" {
			st = ""
		}
		fn = func(p *plan.Plan) error { plan.Patch{Status: &st}.Apply(p, by); return nil }
	case "priority":
		n, err := strconv.Atoi(rest)
		if err != nil {
			return errors.New("priority must be a number 0-3")
		}
		fn = func(p *plan.Plan) error { p.Priority = n; return nil }
	case "note":
		if err := need(); err != nil {
			return err
		}
		fn = func(p *plan.Plan) error { p.AddLog(by, rest); return nil }
	case "task":
		if err := need(); err != nil {
			return err
		}
		fn = func(p *plan.Plan) error { p.Tasks = append(p.Tasks, plan.Task{Text: rest}); return nil }
	case "doing", "done", "undo", "drop":
		if err := need(); err != nil {
			return err
		}
		fn = func(p *plan.Plan) error {
			i, err := plan.FindTask(p.Tasks, rest)
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
	if _, err := plans.Update(it.Rel, fn); err != nil {
		return err
	}
	fmt.Printf("%s: %s ok\n", it.Rel, cmd)
	return nil
}

// printPlan is the one view of a plan: the header everything prints, then
// whatever the project has.
func printPlan(w io.Writer, rel string, p plan.Plan) {
	fmt.Fprintln(w, header(rel, p.Status, p.Priority))
	if p.Empty() {
		fmt.Fprintln(w, "no plan yet: try `loods plan next \"…\"`")
		return
	}
	if p.Next != "" {
		fmt.Fprintf(w, "next:   %s\n", p.Next)
	}
	if len(p.Tasks) > 0 {
		fmt.Fprintln(w, "tasks:")
		for _, t := range p.Tasks {
			fmt.Fprintf(w, "  #%-3d %s %s\n", t.ID, t.Box(), t.Text)
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

// header names a project and, when it has them, its status and priority.
func header(rel, status string, priority int) string {
	out := []string{rel}
	if status != "" {
		out = append(out, status)
	}
	if priority > 0 {
		out = append(out, fmt.Sprintf("P%d", priority))
	}
	return strings.Join(out, "  ·  ")
}
