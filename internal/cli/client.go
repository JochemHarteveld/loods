package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"loods/internal/run"
)

// What the CLI asks a running board to do. It is a short list on purpose:
// anything that only writes state goes straight to the files, and only the
// things that need a session — which must outlive this command and belong to
// the dock — go over HTTP.

// ServerRunning reports whether a loods is answering on this port.
func ServerRunning(port int) bool {
	c := http.Client{Timeout: 500 * time.Millisecond}
	resp, err := c.Get(url(port) + "/api/ping")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b) == "loods"
}

func url(port int) string { return fmt.Sprintf("http://127.0.0.1:%d", port) }

// warnNoServer says what will not happen: the state is written either way, but
// only a running loods starts the sessions.
func warnNoServer(env Env) {
	if ServerRunning(env.Port) {
		return
	}
	fmt.Fprintln(os.Stderr, "loods: no server on this port, so no sessions are started."+
		" Run `loods` and the run picks up where it is.")
}

// startRunOnServer asks the running loods to make the run and start the
// orchestrator. It has to be the server: the session must outlive this command
// and belong to the dock, which is where you watch it.
func startRunOnServer(env Env, rel, goal string, maxPar int) (run.Run, error) {
	if !ServerRunning(env.Port) {
		return run.Run{}, fmt.Errorf("no loods server on %s", url(env.Port))
	}
	body, _ := json.Marshal(map[string]any{"project": rel, "goal": goal, "max_parallel": maxPar})
	req, err := http.NewRequest(http.MethodPost, url(env.Port)+"/api/runs/start", bytes.NewReader(body))
	if err != nil {
		return run.Run{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Loods", "1")
	c := http.Client{Timeout: 15 * time.Second} // the server may rescan first
	resp, err := c.Do(req)
	if err != nil {
		return run.Run{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// An error body is not to be trusted for length.
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return run.Run{}, errors.New(strings.TrimSpace(string(msg)))
	}
	var r run.Run
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return run.Run{}, err
	}
	return r, nil
}
