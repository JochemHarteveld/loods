package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"loods/internal/deploy"
	"loods/internal/textfmt"
	"loods/internal/xdg"
)

const deployUsage = `usage: loods deploy [command] [-p group/project]

Publish the project you are in at <project>.%s. The hostname already
resolves — one wildcard record covers every project — so publishing is a build,
a route, and nothing else.

  loods deploy                 build and publish this project
  loods deploy list [--all]    what is published, and where it stands
  loods deploy log             the build and the process output
  loods deploy stop            take it offline, keep the hostname
  loods deploy rm              forget it altogether
  loods deploy open            print its URL

  -p group/project   another project
  --json             machine-readable (list)

What a project publishes is worked out from the project itself, or written
down in %s.
`

func runDeploy(env Env, args []string) error {
	sub, rest := "", args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, rest = args[0], args[1:]
	}
	fs := flag.NewFlagSet("deploy", flag.ContinueOnError)
	proj := fs.String("p", "", "project, relative to the root")
	all := fs.Bool("all", false, "every project (list)")
	asJSON := fs.Bool("json", false, "machine-readable output")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, deployUsage, deploy.DefaultDomain, xdg.DeploysConfigPath())
	}
	if err := fs.Parse(rest); errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return err
	}

	store := deploy.NewStore(xdg.DeploysPath())
	store.Refresh()

	if sub == "list" {
		return listDeploys(store, env, *proj, *all, *asJSON)
	}

	it, err := locate(env, *proj)
	if err != nil {
		return err
	}
	rel := it.Rel
	switch sub {
	case "", "deploy":
		return publish(env, rel)
	case "log":
		return deployLog(env, rel)
	case "stop", "rm":
		return deployAction(env, rel, sub)
	case "open":
		d, ok := store.Get(rel)
		if !ok {
			return fmt.Errorf("%s is not deployed", rel)
		}
		fmt.Println("https://" + d.Host)
		return nil
	default:
		fs.Usage()
		return fmt.Errorf("unknown command %q", sub)
	}
}

// publish asks the running board to build and route it. It has to be the
// server: the build and the process it may become must outlive this command
// and belong to the dock, which is where you watch a build go wrong.
func publish(env Env, rel string) error {
	if !ServerRunning(env.Port) {
		return fmt.Errorf("no loods server on %s: run `loods`, then deploy again", url(env.Port))
	}
	var d deploy.Deploy
	if err := postJSON(env, "/api/deploys/start", map[string]any{"project": rel}, &d); err != nil {
		return err
	}
	fmt.Printf("%s  %s\n", d.Host, d.Status)
	switch d.Status {
	case deploy.StatusBuilding:
		fmt.Printf("  building (%s). Follow it: loods deploy log -p %s\n", d.Kind, rel)
	case deploy.StatusLive:
		fmt.Printf("  https://%s\n", d.Host)
	}
	return nil
}

func deployAction(env Env, rel, action string) error {
	if !ServerRunning(env.Port) {
		return fmt.Errorf("no loods server on %s: run `loods`, then try again", url(env.Port))
	}
	var d deploy.Deploy
	if err := postJSON(env, "/api/deploys/"+action, map[string]any{"project": rel}, &d); err != nil {
		return err
	}
	if action == "rm" {
		fmt.Printf("%s is no longer published\n", d.Host)
		return nil
	}
	fmt.Printf("%s  %s\n", d.Host, d.Status)
	return nil
}

func deployLog(env Env, rel string) error {
	if !ServerRunning(env.Port) {
		return fmt.Errorf("no loods server on %s", url(env.Port))
	}
	resp, err := http.Get(url(env.Port) + "/api/deploys/log?project=" + rel)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return errors.New(strings.TrimSpace(string(msg)))
	}
	_, err = io.Copy(os.Stdout, resp.Body)
	return err
}

func listDeploys(store *deploy.Store, env Env, proj string, all, asJSON bool) error {
	list := store.List()
	if !all {
		if it, err := locate(env, proj); err == nil {
			var mine []deploy.Deploy
			for _, d := range list {
				if d.Project == it.Rel {
					mine = append(mine, d)
				}
			}
			list = mine
		}
	}
	if asJSON {
		b, err := json.MarshalIndent(list, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}
	if len(list) == 0 {
		fmt.Println("nothing published yet")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "HOSTNAME\tPROJECT\tKIND\tSTATE\tSINCE\tWHAT")
	for _, d := range list {
		since := ""
		if !d.LiveAt.IsZero() {
			since = textfmt.Ago(d.LiveAt, time.Now())
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			d.Host, d.Project, d.Kind, state(d), since, what(d))
	}
	return w.Flush()
}

// state is the word on the card: what the board thinks, and whether anyone can
// actually reach it right now.
func state(d deploy.Deploy) string {
	if d.Status == deploy.StatusFailed && d.Live() {
		return "failed (serving the last build)"
	}
	return d.Status
}

func what(d deploy.Deploy) string {
	switch {
	case d.Note != "":
		return d.Note
	case d.Upstream != "":
		return "-> " + d.Upstream
	case d.Dir != "":
		return d.Dir
	}
	return ""
}

func postJSON(env Env, path string, body any, out any) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, url(env.Port)+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Loods", "1")
	c := http.Client{Timeout: 30 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return errors.New(strings.TrimSpace(string(msg)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
