package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/coder/websocket"

	"loods/internal/config"
	"loods/internal/proc"
	"loods/internal/project"
)

// startProc runs a project command or a stack. Like open, the client only
// names things loods detected or the user configured; it never sends a command
// line.
func (s *Server) startProc(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Project string `json:"project"`
		Command string `json:"command"` // empty: the project's default
		Stack   string `json:"stack"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var err error
	if req.Stack != "" {
		err = s.startStack(req.Stack)
	} else {
		var spec proc.Spec
		if spec, err = s.projectSpec(req.Project, req.Command); err == nil {
			err = s.procs.Start(spec)
		}
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// projectSpec turns a project id and a command name into something to run.
func (s *Server) projectSpec(rel, command string) (proc.Spec, error) {
	it := s.find(rel)
	if it == nil {
		return proc.Spec{}, errors.New("unknown project " + rel)
	}
	if command == "" {
		command = it.DefaultCommand
	}
	i := slices.IndexFunc(it.Commands, func(c project.Command) bool { return c.Name == command })
	if i < 0 {
		return proc.Spec{}, fmt.Errorf("%s has no command %q", it.Name, command)
	}
	c := it.Commands[i]
	return proc.Spec{
		ID: it.Rel + "#" + c.Name, Project: it.Rel, Name: c.Name, Run: c.Run,
		Dir: filepath.Join(it.Path, c.Dir), Keys: c.Keys, URL: c.URL,
	}, nil
}

// startStack starts one script, or every project command a stack names. A
// command that is already running is not an error: the stack is a shortcut,
// not a transaction.
func (s *Server) startStack(name string) error {
	s.mu.RLock()
	i := slices.IndexFunc(s.stacks, func(st config.Stack) bool { return st.Name == name })
	var st config.Stack
	if i >= 0 {
		st = s.stacks[i]
	}
	s.mu.RUnlock()
	if i < 0 {
		return errors.New("unknown stack " + name)
	}
	if st.Run != "" {
		if _, err := os.Stat(st.Dir); err != nil {
			return err
		}
		return s.procs.Start(proc.Spec{ID: "stack:" + st.Name, Name: st.Name, Run: st.Run, Dir: st.Dir})
	}
	var errs []error
	for _, ref := range st.Commands {
		rel, cmd, _ := strings.Cut(ref, ":")
		spec, err := s.projectSpec(rel, cmd)
		if err == nil {
			err = s.procs.Start(spec)
		}
		if err != nil && !strings.Contains(err.Error(), "already running") {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Server) procAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID   string `json:"id"`
		Data string `json:"data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var err error
	switch r.PathValue("action") {
	case "stop":
		err = s.procs.Stop(req.ID)
	case "restart":
		err = s.procs.Restart(req.ID)
	case "remove":
		err = s.procs.Remove(req.ID)
	case "input":
		err = s.procs.Input(req.ID, []byte(req.Data))
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// term streams a process terminal over a websocket: binary frames out
// (raw pty bytes), JSON messages in ({"type":"input","data":…} or {"type":"resize",…}).
func (s *Server) term(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	backlog, ch, detach, ok := s.procs.Attach(id)
	if !ok {
		http.Error(w, "unknown process", http.StatusNotFound)
		return
	}
	defer detach()
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.devOrigins})
	if err != nil {
		return
	}
	defer c.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	go func() {
		defer cancel()
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			var msg struct {
				Type       string `json:"type"`
				Data       string `json:"data"`
				Cols, Rows int
			}
			if json.Unmarshal(data, &msg) != nil {
				continue
			}
			switch msg.Type {
			case "input":
				s.procs.Input(id, []byte(msg.Data))
			case "resize":
				s.procs.Resize(id, msg.Cols, msg.Rows)
			}
		}
	}()

	if err := c.Write(ctx, websocket.MessageBinary, backlog); err != nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case b, ok := <-ch:
			if !ok {
				c.Close(websocket.StatusNormalClosure, "detached")
				return
			}
			if err := c.Write(ctx, websocket.MessageBinary, gather(ctx, b, ch)); err != nil {
				return
			}
		}
	}
}

// gather collects output until it goes quiet briefly, and sends it as one
// frame. Programs redraw in bursts of tiny writes (docker compose's menu bar:
// a dozen writes over ~2ms per log line); sent one by one, the browser paints
// half-finished redraws, which flickers.
func gather(ctx context.Context, first []byte, ch chan []byte) []byte {
	// Chunks are shared between viewers, so build a fresh buffer.
	b := append([]byte(nil), first...)
	flush := time.After(16 * time.Millisecond)
	for len(b) < 1<<20 {
		quiet := time.NewTimer(3 * time.Millisecond)
		select {
		case next, ok := <-ch:
			if !ok {
				quiet.Stop()
				return b
			}
			if bytes.HasPrefix(next, []byte("\x1bc")) {
				b = b[:0] // a resync replaces everything before it
			}
			b = append(b, next...)
		case <-quiet.C:
			quiet.Stop()
			return b
		case <-flush:
			quiet.Stop()
			return b
		case <-ctx.Done():
			quiet.Stop()
			return b
		}
		quiet.Stop()
	}
	return b
}
