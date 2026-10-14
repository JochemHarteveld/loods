package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"loods/internal/run"
	"loods/internal/textfmt"
	"loods/internal/xdg"
)

// `loods run …`: everything an orchestrator and its job agents do to a run.
// The CLI only writes state — it never starts a session. The server picks the
// file up within two seconds and schedules what may run.

const runUsage = `usage: loods run [-p group/project] [command]

  (none) | list       the runs of this project (--all for every project)
  new <goal>          start a run with no jobs yet; prints its id
  submit <run>        give a planning run its graph: --file jobs.yaml (or -)
  extend <run>        add jobs to a run that is already going (same format)
  status <run>        the run and its jobs (--watch to follow it)
  step <run>:<job> <n>  step n of this job's checklist is done
  done <run>:<job>    this job finished: --note "what you did"
  fail <run>:<job>    this job could not finish: --note "why"
  cancel <run>        stop handing out work; running jobs are stopped too

A graph is a YAML list of jobs, each with an id, a title, a prompt and a list
of steps, and optionally needs: [other ids], role:, exclusive: repo,
isolation: worktree, owns: [paths] and task: <n>:

  jobs:
    - id: stories
      role: stories
      title: Write the user stories
      prompt: |
        Write docs/userstories.md …
      steps:
        - List the user roles
        - Write the stories of the first role
        - Write the stories of the other roles
        - Add acceptance criteria to every story
    - id: tickets
      needs: [stories]
      title: Turn the stories into tasks
      prompt: |
        Read docs/userstories.md and add one task per story …
      steps:
        - Read docs/userstories.md
        - Add one task per story with 'loods todo add'
        - Report the task numbers

The steps are the job's checklist: the agent ticks one off with
'loods run step <run>:<job> <n>' as it finishes it, and the board turns them
into a percentage and a forecast of the time left. Keep them small and of
roughly equal size — the steps a job has finished are what the forecast for the
ones it has left is based on.

loods starts a job as soon as everything it needs is done, up to --max-parallel
sessions at a time, and never two jobs holding the same exclusive lock.

owns: [paths] is the files a job is the only one allowed to change, as globs
relative to the project ("src/lib/ui", "e2e/mobile-*.spec.ts", "docs/**"). Two
jobs that can run at the same time and claim the same files are refused when
the graph is submitted, which is the cheap moment to find out: a boundary that
only lives in a design doc is one two lanes walk over, and the merge job pays
for it. Jobs that wait for each other may share files — taking turns is what a
sequence is for. A folder owns its whole subtree; a name with a dot in it is a
file. The field is optional, and a job without it claims nothing.

isolation: worktree gives a job a git worktree and a branch of its own
(loods/<run>/<job>), so it can commit while another session commits in the
project folder: its exclusive lock is scoped to its own tree, which is how
jobs that all ask for exclusive: repo still run at the same time. The branch
and the worktree are left behind for a later job to merge.
`

type runFlags struct {
	project string
	all     bool
	file    string
	note    string
	maxPar  int
	watch   bool
	asJSON  bool
}

func runRun(env Env, args []string) error {
	var f runFlags
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.StringVar(&f.project, "p", "", "project path relative to the root")
	fs.BoolVar(&f.all, "all", false, "every project, not just this one")
	fs.StringVar(&f.file, "file", "", "graph of jobs to submit or extend with (- for stdin)")
	fs.StringVar(&f.note, "note", "", "what you did, or why it failed")
	fs.IntVar(&f.maxPar, "max-parallel", 0, fmt.Sprintf("sessions at a time (default %d)", run.DefaultMaxPar))
	fs.BoolVar(&f.watch, "watch", false, "keep printing the run until it is over")
	fs.BoolVar(&f.asJSON, "json", false, "machine-readable output")
	fs.Usage = func() { fmt.Fprint(os.Stderr, runUsage) }

	// Flags before or after the words, like `loods todo`: an agent types
	// `loods run done 4f2a1c:stories --note "…"`.
	words, err := parseInterleaved(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return err
	}
	cmd, rest := "list", words
	if len(words) > 0 {
		cmd, rest = words[0], words[1:]
	}

	runs := run.NewStore(xdg.RunsPath())
	runs.Refresh()

	one := func(what string) error {
		if len(rest) != 1 {
			fs.Usage()
			return fmt.Errorf("%s needs one %s", cmd, what)
		}
		return nil
	}
	switch cmd {
	case "list":
		return runList(env, runs, f)
	case "new":
		if len(rest) == 0 {
			fs.Usage()
			return errors.New("new needs the goal of the run")
		}
		return runNewRun(env, runs, strings.Join(rest, " "), f)
	case "submit", "extend":
		if err := one("run id"); err != nil {
			return err
		}
		return runSubmit(env, runs, cmd, rest[0], f)
	case "status":
		if err := one("run id"); err != nil {
			return err
		}
		return runStatus(runs, rest[0], f)
	case "step":
		if len(rest) != 2 {
			fs.Usage()
			return errors.New("step needs a job and a step number, like 4f2a1c:stories 2")
		}
		return runStep(runs, rest[0], rest[1], f)
	case "done", "fail":
		if err := one("job, like 4f2a1c:stories"); err != nil {
			return err
		}
		return runFinish(runs, cmd, rest[0], f)
	case "cancel":
		if err := one("run id"); err != nil {
			return err
		}
		return runCancel(env, runs, rest[0], f)
	default:
		fs.Usage()
		return fmt.Errorf("unknown run command %q", cmd)
	}
}

// pick finds a run by its id, or by an unambiguous start of it, so you can type
// the first few characters you see on the board.
func pick(runs *run.Store, id string) (run.Run, error) {
	id = strings.TrimSpace(id)
	if r, ok := runs.Get(id); ok {
		return r, nil
	}
	var hits []run.Run
	for _, r := range runs.All() {
		if id != "" && strings.HasPrefix(r.ID, id) {
			hits = append(hits, r)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return run.Run{}, fmt.Errorf("no run %q (`loods run list --all` shows them)", id)
	default:
		ids := make([]string, 0, len(hits))
		for _, r := range hits {
			ids = append(ids, r.ID)
		}
		sort.Strings(ids)
		return run.Run{}, fmt.Errorf("%q matches several runs: %s", id, strings.Join(ids, ", "))
	}
}

func runList(env Env, runs *run.Store, f runFlags) error {
	list := make([]run.Run, 0)
	if f.all {
		for _, r := range runs.All() {
			list = append(list, r)
		}
		run.Sort(list)
	} else {
		it, err := locate(env, f.project)
		if err != nil {
			return err
		}
		list = runs.Of(it.Rel)
	}
	if f.asJSON {
		return writeJSON(os.Stdout, list)
	}
	if len(list) == 0 {
		fmt.Println("no runs yet: try `loods new \"<what to build>\"`")
		return nil
	}
	for _, r := range list {
		fmt.Printf("%s  %s  %s  %s  %s\n", r.ID, textfmt.OrDash(r.Project), r.Status, tally(r), textfmt.Trim(r.Goal, 60))
	}
	return nil
}

func runNewRun(env Env, runs *run.Store, goal string, f runFlags) error {
	it, err := locate(env, f.project)
	if err != nil {
		return err
	}
	r, err := runs.New(it.Rel, goal, f.maxPar)
	if err != nil {
		return err
	}
	if f.asJSON {
		return writeJSON(os.Stdout, r)
	}
	fmt.Printf("run %s on %s: %s\n", r.ID, r.Project, r.Goal)
	fmt.Printf("submit its graph with `loods run submit %s --file jobs.yaml`\n", r.ID)
	warnNoServer(env)
	return nil
}

func runSubmit(env Env, runs *run.Store, cmd, id string, f runFlags) error {
	r, err := pick(runs, id)
	if err != nil {
		return err
	}
	if f.file == "" {
		return errors.New("say where the jobs are: --file jobs.yaml, or --file - to read them from stdin")
	}
	jobs, err := readJobs(f.file)
	if err != nil {
		return err
	}
	if cmd == "submit" {
		r, err = runs.Submit(r.ID, jobs)
	} else {
		r, err = runs.Extend(r.ID, jobs)
	}
	if err != nil {
		return err
	}
	if f.asJSON {
		return writeJSON(os.Stdout, r)
	}
	fmt.Printf("run %s: %d jobs, %s\n", r.ID, len(r.Jobs), r.Status)
	printRun(os.Stdout, r)
	warnNoServer(env)
	return nil
}

func runStatus(runs *run.Store, id string, f runFlags) error {
	r, err := pick(runs, id)
	if err != nil {
		return err
	}
	if f.asJSON && !f.watch {
		return writeJSON(os.Stdout, r)
	}
	if !f.watch {
		printRun(os.Stdout, r)
		return nil
	}
	// --watch is for the orchestrator: print again only when something moved,
	// and stop when the run is over so the session is not left hanging.
	last := ""
	for {
		runs.Refresh()
		r, err := pick(runs, r.ID)
		if err != nil {
			return err
		}
		var b strings.Builder
		printRun(&b, r)
		if b.String() != last {
			last = b.String()
			fmt.Print(b.String())
		}
		if r.Over() {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
}

// runStep is the one line a job agent runs as it works: step n is done. It
// prints the job's own line back, so the agent sees the percentage it just
// moved instead of the whole run.
func runStep(runs *run.Store, ref, nth string, f runFlags) error {
	runID, job, err := run.ParseRef(ref)
	if err != nil {
		return err
	}
	n, err := strconv.Atoi(strings.TrimSpace(nth))
	if err != nil {
		return fmt.Errorf("%q is not a step number", nth)
	}
	r, err := pick(runs, runID)
	if err != nil {
		return err
	}
	r, err = runs.Step(r.ID, job, n)
	if err != nil {
		return err
	}
	if f.asJSON {
		return writeJSON(os.Stdout, r)
	}
	i, _ := r.JobIndex(job)
	j := r.Jobs[i]
	fmt.Printf("%s:%s  %s  %s\n", r.ID, job, progress(j, time.Now()), textfmt.Trim(j.Steps[n-1].Text, 60))
	printSteps(os.Stdout, j)
	return nil
}

func runFinish(runs *run.Store, cmd, ref string, f runFlags) error {
	runID, job, err := run.ParseRef(ref)
	if err != nil {
		return err
	}
	r, err := pick(runs, runID)
	if err != nil {
		return err
	}
	status := run.JobDone
	if cmd == "fail" {
		status = run.JobFailed
	}
	if f.note == "" {
		return fmt.Errorf("say what happened: loods run %s %s --note \"…\"", cmd, ref)
	}
	r, err = runs.Finish(r.ID, job, status, f.note)
	if err != nil {
		return err
	}
	if f.asJSON {
		return writeJSON(os.Stdout, r)
	}
	fmt.Printf("%s:%s %s\n", r.ID, job, status)
	printRun(os.Stdout, r)
	return nil
}

func runCancel(env Env, runs *run.Store, id string, f runFlags) error {
	r, err := pick(runs, id)
	if err != nil {
		return err
	}
	r, err = runs.Cancel(r.ID, f.note)
	if err != nil {
		return err
	}
	fmt.Printf("run %s cancelled\n", r.ID)
	// The sessions themselves belong to the server; it stops them on its next
	// pass. Without one, they keep running wherever they were started.
	warnNoServer(env)
	return nil
}

// readJobs reads a graph from a file or stdin.
func readJobs(path string) ([]run.Job, error) {
	var b []byte
	var err error
	if path == "-" {
		b, err = io.ReadAll(os.Stdin)
	} else {
		b, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, err
	}
	return run.ParseJobs(b)
}

// printRun is the one view of a run: a header, then a line per job with why it
// is waiting, so `status` and `--watch` show the same thing the Agents view does.
func printRun(w io.Writer, r run.Run) {
	now := time.Now()
	fmt.Fprintf(w, "run %s  ·  %s  ·  %s  ·  %s%s\n",
		r.ID, textfmt.OrDash(r.Project), r.Status, tally(r), left(r, now))
	fmt.Fprintf(w, "goal:   %s\n", r.Goal)
	if len(r.Jobs) == 0 {
		fmt.Fprintf(w, "no jobs yet: `loods run submit %s --file jobs.yaml`\n", r.ID)
		return
	}
	fmt.Fprintln(w, "jobs:")
	for _, j := range r.Jobs {
		fmt.Fprintf(w, "  %-12s %-10s %-9s %-7s %-6s %s\n",
			textfmt.Trim(j.ID, 12), textfmt.Trim(j.Role, 10), j.Status,
			progress(j, now), clock(j, now), why(r, j))
		printSteps(w, j)
	}
}

// printSteps is the checklist under a job: a tick for what is done, so the
// terminal shows the same thing the Agents view does. A job without a
// checklist prints nothing extra.
func printSteps(w io.Writer, j run.Job) {
	for i, s := range j.Steps {
		mark := " "
		if s.Done {
			mark = "✓"
		}
		fmt.Fprintf(w, "    %s %d. %s\n", mark, i+1, textfmt.Trim(s.Text, 70))
	}
}

// progress is the job's percentage, and how much longer it is expected to
// need. Both are forecasts from its own checklist: a job without one shows its
// status only, which is all it ever told anybody.
func progress(j run.Job, now time.Time) string {
	if len(j.Steps) == 0 {
		return ""
	}
	s := fmt.Sprintf("%d%%", int(j.Progress()*100+0.5))
	if d, ok := j.ETA(now); ok {
		s += " " + about(d)
	}
	return s
}

// about is a forecast as a reader should hear it: "~4m", but "<1m" on its own,
// because the "<" is already the hedge the tilde is there for.
func about(d time.Duration) string {
	s := textfmt.Dur(d)
	if strings.HasPrefix(s, "<") {
		return s
	}
	return "~" + s
}

// left is the run's forecast, as the header's last field.
func left(r run.Run, now time.Time) string {
	if d, ok := r.ETA(now); ok {
		return fmt.Sprintf("  ·  %d%% · %s left", int(r.Progress()*100+0.5), about(d))
	}
	if len(r.Jobs) > 0 && !r.Over() {
		return fmt.Sprintf("  ·  %d%%", int(r.Progress()*100+0.5))
	}
	return ""
}

// why is the right-hand column: the note an agent left, or what the job is
// still waiting for.
func why(r run.Run, j run.Job) string {
	if j.Note != "" {
		return textfmt.Trim(j.Note, 70)
	}
	if j.Status != run.JobPending {
		return textfmt.Trim(j.Title, 70)
	}
	if waiting := r.Waiting(j); len(waiting) > 0 {
		return "waits for " + strings.Join(waiting, ", ")
	}
	// Ready, unless another job of this run is holding the lock it needs.
	if holder := r.LockHolder(j); holder != "" {
		return "waits for a free " + j.Exclusive + " (" + holder + " has it)"
	}
	return "ready"
}

func clock(j run.Job, now time.Time) string {
	switch {
	case j.Status == run.JobRunning && !j.StartedAt.IsZero():
		return textfmt.Ago(j.StartedAt, now)
	case !j.EndedAt.IsZero() && !j.StartedAt.IsZero():
		return textfmt.Ago(j.StartedAt, j.EndedAt)
	default:
		return ""
	}
}

func tally(r run.Run) string {
	done, running := r.Tally()
	s := fmt.Sprintf("%d/%d done", done, len(r.Jobs))
	if running > 0 {
		s += fmt.Sprintf(", %d working", running)
	}
	return s
}
