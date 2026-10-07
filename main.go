// loods: one board for all projects under ~/Projects — activity, git state,
// branches, and shortcuts to open them. Absorbs graveyard (archive/trash/undo).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	home, _ := os.UserHomeDir()
	depth := flag.Int("depth", 3, "how many folder levels to descend looking for projects")
	port := flag.Int("port", 7777, "port on 127.0.0.1")
	noOpen := flag.Bool("no-open", false, "do not open the app window")
	dev := flag.Bool("dev", false, "also accept terminal websockets from the vite dev server on :5173")
	jsonOut := flag.Bool("json", false, "scan, print JSON and exit")
	archive := flag.String("archive", filepath.Join(home, "Archive"), "where archived projects are moved")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `usage:
  loods [flags] [root]      serve the board for projects under root (default ~/Projects, or $LOODS_ROOT)
  loods plan [command]      show or edit the plan of the project you are in (loods plan -h)
  loods claude install      add the /wrapup skill and a SessionStart hook to Claude Code
  loods claude uninstall    remove them again
  loods hook                the SessionStart hook itself (reads Claude's JSON on stdin)
  loods undo                restore the last archived batch

flags:
`)
		flag.PrintDefaults()
	}
	flag.Parse()

	archiveRoot, _ := filepath.Abs(*archive)
	root := filepath.Join(home, "Projects")
	if env := os.Getenv("LOODS_ROOT"); env != "" {
		root = env
	}
	switch flag.Arg(0) {
	case "undo":
		if err := runUndo(*archive); err != nil {
			fail(err)
		}
		return
	case "plan":
		if err := runPlan(flag.Args()[1:], root, archiveRoot, *depth); err != nil {
			fail(err)
		}
		return
	case "hook":
		runHook(root, archiveRoot, *depth)
		return
	case "claude":
		if err := runClaude(flag.Args()[1:]); err != nil {
			fail(err)
		}
		return
	}
	if flag.NArg() > 0 {
		root = flag.Arg(0)
	}
	root, _ = filepath.Abs(root)

	if *jsonOut {
		items := Discover(root, *depth, archiveRoot)
		markDupes(items)
		scanAll(items, true)
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(items); err != nil {
			fail(err)
		}
		return
	}

	url := fmt.Sprintf("http://127.0.0.1:%d", *port)
	if alreadyRunning(url) {
		// Second launch (e.g. from a hotkey): just show the window.
		if err := openWindow(url); err != nil {
			fail(err)
		}
		return
	}

	s := newServer(root, archiveRoot, *depth, *port, stateFile())
	if *dev {
		s.devOrigins = []string{"localhost:5173", "127.0.0.1:5173"}
	}
	ln, err := s.listen()
	if err != nil {
		fail(err)
	}
	go s.indexClaude()
	go s.loop(30 * time.Second)
	go s.statsLoop(2 * time.Second)
	fmt.Printf("loods: %s  (root %s, config %s)\n", url, root, s.cfgPath)
	if !*noOpen {
		if err := openWindow(url); err != nil {
			fmt.Fprintln(os.Stderr, "open window:", err)
		}
	}

	// Ctrl+C / logout: stop everything started from the Garage, then exit.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	srv := &http.Server{Handler: s.handler()}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			fail(err)
		}
	}()
	<-ctx.Done()
	stop()
	fmt.Println("\nloods: stopping processes…")
	s.procs.StopAll(12 * time.Second)
	srv.Close()
}

func stateFile() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "loods", "procs.json")
}

func alreadyRunning(url string) bool {
	c := http.Client{Timeout: 500 * time.Millisecond}
	resp, err := c.Get(url + "/api/ping")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b) == "loods"
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
