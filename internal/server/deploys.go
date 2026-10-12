package server

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"loods/internal/deploy"
	"loods/internal/gitx"
	"loods/internal/proc"
	"loods/internal/project"
	"loods/internal/xdg"
)

// Publishing happens here, in the server, for the same reason runs do: a
// deploy's build and its process have to outlive the command that asked for
// them and belong to the dock. `loods deploy` only asks; this decides what to
// run, hands it to the process manager, and writes down what it did.
//
// A deploy is an ordinary loods process. That is the whole trick: it gets the
// terminal, the output backlog, the stop sequence and the orphan recovery that
// a dev server already had, and the dock shows a build going wrong the same
// way it shows anything else going wrong.

// deployProcID is the process that builds and runs one project's deploy.
func deployProcID(rel string) string { return rel + "#deploy" }

// startDeploy is `loods deploy` and the board's deploy button.
func (s *Server) startDeploy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Project string `json:"project"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	it := s.find(req.Project)
	if it == nil {
		http.Error(w, "unknown project", http.StatusNotFound)
		return
	}
	d, err := s.publishDeploy(it)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.publish(false)
	writeJSON(w, d)
}

// deployAction is stop and rm. Both take the deploy offline; rm also forgets
// the hostname.
func (s *Server) deployAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Project string `json:"project"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	action := r.PathValue("action")
	s.stopIfAlive(deployProcID(req.Project))

	switch action {
	case "stop":
		d, err := s.deploys.Put(req.Project, func(d *deploy.Deploy) error {
			// Taking it offline means the edge has nothing to send anyone to;
			// the page it writes then says stopped instead of 404.
			d.Dir, d.Upstream = "", ""
			d.Status, d.Note = deploy.StatusStopped, ""
			return nil
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.publish(false)
		writeJSON(w, d)
	case "rm":
		d, found, err := s.deploys.Remove(req.Project)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if !found {
			http.Error(w, "not deployed", http.StatusNotFound)
			return
		}
		s.procs.Remove(deployProcID(req.Project))
		s.publish(false)
		writeJSON(w, d)
	default:
		http.Error(w, "unknown action", http.StatusNotFound)
	}
}

// deployLog is the build and the process output as the dock has it, for a
// terminal that is not the dock.
func (s *Server) deployLog(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("project")
	backlog, _, detach, ok := s.procs.Attach(deployProcID(rel))
	if !ok {
		http.Error(w, "no deploy has run for this project", http.StatusNotFound)
		return
	}
	detach()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(backlog)
}

// publishDeploy works out how to publish a project, writes down that it is
// being published, and starts the one process that does it.
func (s *Server) publishDeploy(it *project.Item) (deploy.Deploy, error) {
	cfg, err := deploy.LoadConfig(xdg.DeploysConfigPath())
	if err != nil {
		return deploy.Deploy{}, err
	}
	rec, err := deploy.Resolve(cfg, it.Rel, it.Path)
	if err != nil {
		return deploy.Deploy{}, err
	}

	port := 0
	if rec.Kind == deploy.KindProc {
		if port, err = s.deploys.FreePort(it.Rel, portListening); err != nil {
			return deploy.Deploy{}, err
		}
	}
	cmd := deployCommand(rec, port)
	commit, _ := gitx.Run(it.Path, "rev-parse", "--short", "HEAD")

	d, err := s.deploys.Put(it.Rel, func(d *deploy.Deploy) error {
		d.Host, d.Kind, d.Port = rec.Host, rec.Kind, port
		d.SPA = rec.SPA
		d.Commit, d.Branch = strings.TrimSpace(commit), it.Branch
		d.StartedAt = time.Now()
		d.Status, d.Note = deploy.StatusBuilding, ""
		d.ProcID = ""
		// Dir and Upstream are deliberately left as they were: while this
		// build runs, the version that is up stays up.
		if cmd == "" {
			d.Status = deploy.StatusLive
		}
		return nil
	})
	if err != nil {
		return d, err
	}

	// Nothing to build and nothing to run: the directory is already there.
	if cmd == "" {
		return s.settleStatic(it.Rel, rec, "")
	}

	spec := proc.Spec{
		ID:      deployProcID(it.Rel),
		Project: it.Rel,
		Kind:    "deploy",
		Name:    "deploy",
		Run:     cmd,
		Dir:     rec.Dir,
		URL:     "https://" + rec.Host,
	}
	if err := s.procs.Start(spec); err != nil {
		s.failDeploy(it.Rel, err.Error())
		return d, err
	}
	return s.deploys.Put(it.Rel, func(d *deploy.Deploy) error {
		d.ProcID = spec.ID
		return nil
	})
}

// deployCommand is the single shell line that publishes a project: the build,
// and for a proc deploy the server it then becomes. They are one process on
// purpose — a build that fails never becomes a server, and the dock shows both
// halves in one terminal.
func deployCommand(rec deploy.Recipe, port int) string {
	var parts []string
	for _, k := range sortedKeys(rec.Env) {
		parts = append(parts, fmt.Sprintf("export %s=%s;", k, shellQuote(rec.Env[k])))
	}
	if port != 0 {
		parts = append(parts, fmt.Sprintf("export PORT=%d;", port))
	}
	switch {
	case rec.Run != "" && rec.Build != "":
		parts = append(parts, rec.Build, "&&", "exec", rec.Run)
	case rec.Run != "":
		parts = append(parts, "exec", rec.Run)
	case rec.Build != "":
		parts = append(parts, rec.Build)
	default:
		return ""
	}
	return strings.Join(parts, " ")
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// shellQuote makes a value safe to pass through `bash -lc`, which is how every
// loods process is started.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// tickDeploys turns what the processes did into what the edge serves. It runs
// in the same two-second pass as the runs scheduler, for the same reason: the
// board and the proxy should never disagree for longer than that.
func (s *Server) tickDeploys() bool {
	changed := s.deploys.Refresh()
	for _, d := range s.deploys.List() {
		if d.ProcID == "" || d.Status == deploy.StatusStopped {
			continue
		}
		p, ok := s.procs.Find(d.ProcID)
		if !ok {
			continue
		}
		cfg, err := deploy.LoadConfig(xdg.DeploysConfigPath())
		if err != nil {
			continue
		}
		it := s.find(d.Project)
		if it == nil {
			continue
		}
		rec, err := deploy.Resolve(cfg, d.Project, it.Path)
		if err != nil {
			continue
		}

		switch {
		case p.Alive() && d.Kind == deploy.KindProc:
			// The process is the deploy: it being up is it being live.
			if d.Status != deploy.StatusLive || d.Upstream == "" {
				s.deploys.Put(d.Project, func(d *deploy.Deploy) error {
					d.Upstream = fmt.Sprintf("127.0.0.1:%d", d.Port)
					d.Status, d.Note, d.LiveAt = deploy.StatusLive, "", time.Now()
					return nil
				})
				changed = true
			}
		case p.Alive():
			// A build still running. Nothing to decide yet.
		case d.Kind == deploy.KindProc:
			s.endProcDeploy(d, p)
			changed = true
		case d.Status == deploy.StatusBuilding:
			if p.ExitCode != 0 && !p.Stopped {
				s.failDeploy(d.Project, fmt.Sprintf("the build exited %d", p.ExitCode))
			} else if p.Stopped {
				s.failDeploy(d.Project, "the build was stopped")
			} else if _, err := s.settleStatic(d.Project, rec, p.ID); err != nil {
				s.failDeploy(d.Project, err.Error())
			}
			changed = true
		}
	}
	return changed
}

// settleStatic points the hostname at what the build produced, for the kinds
// that leave a directory or a container behind rather than a process.
func (s *Server) settleStatic(rel string, rec deploy.Recipe, procID string) (deploy.Deploy, error) {
	upstream := ""
	dir := ""
	switch rec.Kind {
	case deploy.KindStatic:
		out, err := rec.Output()
		if err != nil {
			return deploy.Deploy{}, err
		}
		dir = out
	case deploy.KindCompose:
		addr, err := deploy.ComposeUpstream(rec.Dir, rec.Service, rec.Port)
		if err != nil {
			return deploy.Deploy{}, err
		}
		upstream = addr
	}
	return s.deploys.Put(rel, func(d *deploy.Deploy) error {
		d.Dir, d.Upstream = dir, upstream
		d.Status, d.Note, d.LiveAt = deploy.StatusLive, "", time.Now()
		if procID != "" {
			d.ProcID = procID
		}
		return nil
	})
}

// endProcDeploy records a server that stopped: on purpose, or by falling over.
func (s *Server) endProcDeploy(d deploy.Deploy, p proc.Proc) {
	s.deploys.Put(d.Project, func(d *deploy.Deploy) error {
		d.Upstream = "" // nothing listens there any more; say so rather than time out
		switch {
		case p.Stopped:
			d.Status, d.Note = deploy.StatusStopped, ""
		case p.ExitCode != 0:
			d.Status = deploy.StatusFailed
			d.Note = fmt.Sprintf("it exited %d", p.ExitCode)
		default:
			d.Status, d.Note = deploy.StatusStopped, "it exited on its own"
		}
		return nil
	})
}

func (s *Server) failDeploy(rel, note string) {
	s.deploys.Put(rel, func(d *deploy.Deploy) error {
		d.Status, d.Note = deploy.StatusFailed, note
		return nil
	})
}

// portListening reports whether something already holds a port on this
// machine, so a deploy is never handed one a dev server took first.
func portListening(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return true
	}
	ln.Close()
	return false
}
