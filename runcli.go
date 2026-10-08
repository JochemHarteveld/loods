package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// `loods run …`: everything an orchestrator and its job agents do to a run.
// The CLI only writes state — it never starts a session. Starting is the
// server's job, because the processes have to outlive the command and show up
// in the dock; the server picks the file up within two seconds and schedules
// what may run (see tickRuns in server.go). Without a server the state is still
// correct, so the commands say so instead of silently doing nothing.

const runUsage = `usage: loods run [-p group/project] [command]

  (none) | list       the runs of this project (--all for every project)
  new <goal>          start a run with no jobs yet; prints its id
  submit <run>        give a planning run its graph: --file jobs.yaml (or -)
  extend <run>        add jobs to a run that is already going (same format)
  status <run>        the run and its jobs (--watch to follow it)
  done <run>:<job>    this job finished: --note "what you did"
  fail <run>:<job>    this job could not finish: --note "why"
  cancel <run>        stop handing out work; running jobs are stopped too

A graph is a YAML list of jobs, each with an id, a title and a prompt, and
optionally needs: [other ids], role:, exclusive: repo and task: <n>:

  jobs:
    - id: stories
      role: stories
      title: Write the user stories
      prompt: |
        Write docs/userstories.md …
    - id: tickets
      needs: [stories]
      title: Turn the stories into tasks
      prompt: |
        Read docs/userstories.md and add one task per story …

loods starts a job as soon as everything it needs is done, up to --max-parallel
sessions at a time, and never two jobs holding the same exclusive lock.
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

func runRun(args []string, root, archive string, depth, port int) error {
	var f runFlags
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.StringVar(&f.project, "p", "", "project path relative to the root")
	fs.BoolVar(&f.all, "all", false, "every project, not just this one")
	fs.StringVar(&f.file, "file", "", "graph of jobs to submit or extend with (- for stdin)")
	fs.StringVar(&f.note, "note", "", "what you did, or why it failed")
	fs.IntVar(&f.maxPar, "max-parallel", 0, fmt.Sprintf("sessions at a time (default %d)", defaultMaxPar))
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

	rs := newRunStore(runsPath())
	rs.Refresh()

	switch cmd {
	case "list":
		return runList(rs, f, root, archive, depth)
	case "new":
		if len(rest) == 0 {
			fs.Usage()
			return errors.New("new needs the goal of the run")
		}
		return runNewCmd(rs, strings.Join(rest, " "), f, root, archive, depth, port)
	case "submit", "extend":
		if len(rest) != 1 {
			fs.Usage()
			return fmt.Errorf("%s needs one run id", cmd)
		}
		return runSubmit(rs, cmd, rest[0], f, port)
	case "status":
		if len(rest) != 1 {
			fs.Usage()
			return errors.New("status needs one run id")
		}
		return runStatus(rs, rest[0], f)
	case "done", "fail":
		if len(rest) != 1 {
			fs.Usage()
			return fmt.Errorf("%s needs one job, like 4f2a1c:stories", cmd)
		}
		return runFinish(rs, cmd, rest[0], f)
	case "cancel":
		if len(rest) != 1 {
			fs.Usage()
			return errors.New("cancel needs one run id")
		}
		return runCancel(rs, rest[0], f, port)
	default:
		fs.Usage()
		return fmt.Errorf("unknown run command %q", cmd)
	}
}

// pick finds a run by its id, or by an unambiguous start of it, so you can type
// the first few characters you see on the board.
func pick(rs *RunStore, id string) (AgentRun, error) {
	id = strings.TrimSpace(id)
	if r, ok := rs.Get(id); ok {
		return r, nil
	}
	var hits []AgentRun
	for _, r := range rs.All() {
		if id != "" && strings.HasPrefix(r.ID, id) {
			hits = append(hits, r)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return AgentRun{}, fmt.Errorf("no run %q (`loods run list --all` shows them)", id)
	default:
		ids := make([]string, 0, len(hits))
		for _, r := range hits {
			ids = append(ids, r.ID)
		}
		sort.Strings(ids)
		return AgentRun{}, fmt.Errorf("%q matches several runs: %s", id, strings.Join(ids, ", "))
	}
}

func runList(rs *RunStore, f runFlags, root, archive string, depth int) error {
	runs := make([]AgentRun, 0)
	if f.all {
		for _, r := range rs.All() {
			runs = append(runs, r)
		}
		sortRuns(runs)
	} else {
		it, err := cliProject(f.project, root, archive, depth)
		if err != nil {
			return err
		}
		runs = rs.Of(it.Rel)
	}
	if f.asJSON {
		return writeJSONTo(os.Stdout, runs)
	}
	if len(runs) == 0 {
		fmt.Println("no runs yet: try `loods new \"<what to build>\"`")
		return nil
	}
	for _, r := range runs {
		fmt.Printf("%s  %s  %s  %s  %s\n", r.ID, orDash(r.Project), r.Status, jobTally(r), trim(r.Goal, 60))
	}
	return nil
}

func runNewCmd(rs *RunStore, goal string, f runFlags, root, archive string, depth, port int) error {
	it, err := cliProject(f.project, root, archive, depth)
	if err != nil {
		return err
	}
	r, err := rs.New(it.Rel, goal, f.maxPar)
	if err != nil {
		return err
	}
	if f.asJSON {
		return writeJSONTo(os.Stdout, r)
	}
	fmt.Printf("run %s on %s: %s\n", r.ID, r.Project, r.Goal)
	fmt.Printf("submit its graph with `loods run submit %s --file jobs.yaml`\n", r.ID)
	warnNoServer(port)
	return nil
}

func runSubmit(rs *RunStore, cmd, id string, f runFlags, port int) error {
	r, err := pick(rs, id)
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
		r, err = rs.Submit(r.ID, jobs)
	} else {
		r, err = rs.Extend(r.ID, jobs)
	}
	if err != nil {
		return err
	}
	if f.asJSON {
		return writeJSONTo(os.Stdout, r)
	}
	fmt.Printf("run %s: %d jobs, %s\n", r.ID, len(r.Jobs), r.Status)
	printRun(os.Stdout, r)
	warnNoServer(port)
	return nil
}

func runStatus(rs *RunStore, id string, f runFlags) error {
	r, err := pick(rs, id)
	if err != nil {
		return err
	}
	if f.asJSON && !f.watch {
		return writeJSONTo(os.Stdout, r)
	}
	if !f.watch {
		printRun(os.Stdout, r)
		return nil
	}
	// --watch is for the orchestrator: print again only when something moved,
	// and stop when the run is over so the session is not left hanging.
	last := ""
	for {
		rs.Refresh()
		r, err := pick(rs, r.ID)
		if err != nil {
			return err
		}
		var b strings.Builder
		printRun(&b, r)
		if b.String() != last {
			last = b.String()
			fmt.Print(b.String())
		}
		if r.over() {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
}

func runFinish(rs *RunStore, cmd, ref string, f runFlags) error {
	run, job, err := parseJobRef(ref)
	if err != nil {
		return err
	}
	r, err := pick(rs, run)
	if err != nil {
		return err
	}
	status := JobDone
	if cmd == "fail" {
		status = JobFailed
	}
	if f.note == "" {
		return fmt.Errorf("say what happened: loods run %s %s --note \"…\"", cmd, ref)
	}
	r, err = rs.Finish(r.ID, job, status, f.note)
	if err != nil {
		return err
	}
	if f.asJSON {
		return writeJSONTo(os.Stdout, r)
	}
	fmt.Printf("%s:%s %s\n", r.ID, job, status)
	printRun(os.Stdout, r)
	return nil
}

func runCancel(rs *RunStore, id string, f runFlags, port int) error {
	r, err := pick(rs, id)
	if err != nil {
		return err
	}
	r, err = rs.Cancel(r.ID, f.note)
	if err != nil {
		return err
	}
	fmt.Printf("run %s cancelled\n", r.ID)
	// The sessions themselves belong to the server; it stops them on its next
	// pass. Without one, they keep running wherever they were started.
	warnNoServer(port)
	return nil
}

// jobInput is the job as a graph file may write it: the fields an orchestrator
// gets to set, and nothing else. Statuses and process ids are loods's to fill
// in, so a file cannot claim a job is already done.
type jobInput struct {
	ID        string   `yaml:"id"`
	Role      string   `yaml:"role"`
	Title     string   `yaml:"title"`
	Prompt    string   `yaml:"prompt"`
	Needs     []string `yaml:"needs"`
	Exclusive string   `yaml:"exclusive"`
	Task      int      `yaml:"task"`
}

// readJobs reads a graph from a file or stdin. Both `jobs:` with a list under
// it and a bare list are accepted, because both are what people write.
func readJobs(path string) ([]Job, error) {
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
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil, errors.New("the graph is empty")
	}
	// Both shapes are accepted because both are what people write.
	var wrapped struct {
		Jobs []jobInput `yaml:"jobs"`
	}
	var bare []jobInput
	in := []jobInput(nil)
	switch {
	case yaml.Unmarshal(b, &wrapped) == nil && len(wrapped.Jobs) > 0:
		in = wrapped.Jobs
	case yaml.Unmarshal(b, &bare) == nil && len(bare) > 0:
		in = bare
	default:
		if err := yaml.Unmarshal(b, &wrapped); err != nil {
			return nil, fmt.Errorf("the graph is not readable: %w", err)
		}
		return nil, errors.New("the graph has no jobs: a list under `jobs:`, or a bare list")
	}
	out := make([]Job, 0, len(in))
	for _, j := range in {
		out = append(out, Job{
			ID: j.ID, Role: j.Role, Title: j.Title, Prompt: j.Prompt,
			Needs: j.Needs, Exclusive: j.Exclusive, Task: j.Task,
			Status: JobPending,
		})
	}
	return out, nil
}

// printRun is the one view of a run: a header, then a line per job with why it
// is waiting, so `status` and `--watch` show the same thing the Agents view does.
func printRun(w io.Writer, r AgentRun) {
	fmt.Fprintf(w, "run %s  ·  %s  ·  %s  ·  %s\n", r.ID, orDash(r.Project), r.Status, jobTally(r))
	fmt.Fprintf(w, "goal:   %s\n", r.Goal)
	if len(r.Jobs) == 0 {
		fmt.Fprintf(w, "no jobs yet: `loods run submit %s --file jobs.yaml`\n", r.ID)
		return
	}
	fmt.Fprintln(w, "jobs:")
	now := time.Now()
	for _, j := range r.Jobs {
		fmt.Fprintf(w, "  %-12s %-10s %-9s %-6s %s\n",
			trim(j.ID, 12), trim(j.Role, 10), j.Status, jobClock(j, now), jobWhy(r, j))
	}
}

// jobWhy is the right-hand column: the note an agent left, or what the job is
// still waiting for.
func jobWhy(r AgentRun, j Job) string {
	if j.Note != "" {
		return trim(j.Note, 70)
	}
	if j.Status != JobPending {
		return trim(j.Title, 70)
	}
	var waiting []string
	for _, need := range j.Needs {
		if i, ok := r.job(need); ok && r.Jobs[i].Status != JobDone {
			waiting = append(waiting, need)
		}
	}
	if len(waiting) > 0 {
		return "waits for " + strings.Join(waiting, ", ")
	}
	// Ready, unless another job of this run is holding the lock it needs.
	for _, other := range r.Jobs {
		if other.Status == JobRunning && other.Exclusive != "" && other.Exclusive == j.Exclusive {
			return "waits for a free " + j.Exclusive + " (" + other.ID + " has it)"
		}
	}
	return "ready"
}

func jobClock(j Job, now time.Time) string {
	switch {
	case j.Status == JobRunning && !j.StartedAt.IsZero():
		return ago(j.StartedAt, now)
	case !j.EndedAt.IsZero() && !j.StartedAt.IsZero():
		return ago(j.StartedAt, j.EndedAt)
	default:
		return ""
	}
}

func jobTally(r AgentRun) string {
	done, running := 0, 0
	for _, j := range r.Jobs {
		switch j.Status {
		case JobDone:
			done++
		case JobRunning:
			running++
		}
	}
	s := fmt.Sprintf("%d/%d done", done, len(r.Jobs))
	if running > 0 {
		s += fmt.Sprintf(", %d working", running)
	}
	return s
}

// warnNoServer says what will not happen: the state is written either way, but
// only a running loods starts the sessions.
func warnNoServer(port int) {
	if alreadyRunning(fmt.Sprintf("http://127.0.0.1:%d", port)) {
		return
	}
	fmt.Fprintln(os.Stderr, "loods: no server on this port, so no sessions are started."+
		" Run `loods` and the run picks up where it is.")
}

func trim(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func writeJSONTo(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// readAllLimit reads an error body without trusting its length.
func readAllLimit(r io.Reader, n int64) (string, error) {
	b, err := io.ReadAll(io.LimitReader(r, n))
	return string(b), err
}
