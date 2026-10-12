// Command loods is one board for every project under ~/Projects: what you were
// working on, what git thinks, what Claude did, what is running, and what is
// next. Run it without arguments to open the board; the subcommands are the
// same thing from a terminal or from an agent.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"loods/internal/cli"
	"loods/internal/desktop"
	"loods/internal/project"
	"loods/internal/server"
	"loods/internal/xdg"
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
		fmt.Fprintf(os.Stderr, "usage:\n"+
			"  loods [flags] [root]      serve the board for projects under root (default ~/Projects, or $LOODS_ROOT)\n")
		cli.WriteHelp(os.Stderr)
		fmt.Fprintf(os.Stderr, "\nflags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	archiveRoot, _ := filepath.Abs(*archive)
	root := filepath.Join(home, "Projects")
	if env := os.Getenv("LOODS_ROOT"); env != "" {
		root = env
	}

	// A subcommand: do that and nothing else.
	if cmd, ok := cli.Lookup(flag.Arg(0)); ok {
		env := cli.Env{Root: root, Archive: archiveRoot, Depth: *depth, Port: *port}
		if err := cmd.Run(env, flag.Args()[1:]); err != nil {
			fail(err)
		}
		return
	}
	if flag.NArg() > 0 {
		root = flag.Arg(0)
	}
	root, _ = filepath.Abs(root)

	if *jsonOut {
		if err := printScan(root, *depth, archiveRoot); err != nil {
			fail(err)
		}
		return
	}

	url := fmt.Sprintf("http://127.0.0.1:%d", *port)
	if cli.ServerRunning(*port) {
		// Second launch (e.g. from a hotkey): just show the window.
		if err := desktop.OpenWindow(url); err != nil {
			fail(err)
		}
		return
	}
	if err := serve(server.Options{
		Root: root, Archive: archiveRoot, Depth: *depth, Port: *port,
		StateFile:  xdg.ProcsPath(),
		DevOrigins: devOrigins(*dev),
	}, url, *noOpen); err != nil {
		fail(err)
	}
}

// serve runs the board until Ctrl+C or logout, then stops everything it started.
func serve(opts server.Options, url string, noOpen bool) error {
	s := server.New(opts)
	ln, err := s.Listen()
	if err != nil {
		return err
	}
	go s.IndexSessions()
	go s.WatchGitHub()
	go s.Scan(30 * time.Second)
	go s.Watch(2 * time.Second)
	fmt.Printf("loods: %s  (root %s, config %s)\n", url, opts.Root, s.ConfigPath())
	if !noOpen {
		if err := desktop.OpenWindow(url); err != nil {
			fmt.Fprintln(os.Stderr, "open window:", err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	srv := &http.Server{Handler: s.Handler()}
	errs := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			errs <- err
		}
	}()
	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}
	stop()
	fmt.Println("\nloods: stopping processes…")
	s.Procs().StopAll(12 * time.Second)
	return srv.Close()
}

// printScan is `loods --json`: one scan of everything under the root, sizes
// included, for a script to read.
func printScan(root string, depth int, archive string) error {
	items := project.Discover(root, depth, archive)
	project.MarkDupes(items)
	project.ScanAll(items, true)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(items)
}

func devOrigins(dev bool) []string {
	if !dev {
		return nil
	}
	return []string{"localhost:5173", "127.0.0.1:5173"}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
