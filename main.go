// loods: one board for all projects under ~/Projects — activity, git state,
// branches, and shortcuts to open them. Absorbs graveyard (archive/trash/undo).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func main() {
	home, _ := os.UserHomeDir()
	depth := flag.Int("depth", 3, "how many folder levels to descend looking for projects")
	port := flag.Int("port", 7777, "port on 127.0.0.1")
	noOpen := flag.Bool("no-open", false, "do not open the app window")
	jsonOut := flag.Bool("json", false, "scan, print JSON and exit")
	archive := flag.String("archive", filepath.Join(home, "Archive"), "where archived projects are moved")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage:\n  loods [flags] [root]   serve the board for projects under root (default ~/Projects)\n  loods undo             restore the last archived batch\n\nflags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.Arg(0) == "undo" {
		if err := runUndo(*archive); err != nil {
			fail(err)
		}
		return
	}

	root := filepath.Join(home, "Projects")
	if flag.NArg() > 0 {
		root = flag.Arg(0)
	}
	root, _ = filepath.Abs(root)
	archiveRoot, _ := filepath.Abs(*archive)

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

	s := newServer(root, archiveRoot, *depth, *port)
	ln, err := s.listen()
	if err != nil {
		fail(err)
	}
	go s.loop(30 * time.Second)
	fmt.Printf("loods: %s  (root %s)\n", url, root)
	if !*noOpen {
		if err := openWindow(url); err != nil {
			fmt.Fprintln(os.Stderr, "open window:", err)
		}
	}
	fail(http.Serve(ln, s.handler()))
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
