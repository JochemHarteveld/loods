package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"loods/internal/edge"
	"loods/internal/xdg"
)

const edgeUsage = `usage: loods edge [flags]

The origin behind the Cloudflare Tunnel: answers for every hostname of a
deploy and routes on Host to what that deploy is — a built directory or a
process on a port. The routes come from the deploys file, which is re-read
whenever it changes, so publishing a project never restarts this.

It binds 127.0.0.1 on purpose: the tunnel is the one way in.

Running it as its own command is what lets the origin move. On this machine
the board starts it; on a server, this command plus cloudflared is the whole
of it.

  --addr <host:port>  where to listen (default %s)
  --routes <file>     the deploys file (default %s)
  --every <duration>  how often to look for changed routes (default %s)
`

const (
	defaultEdgeAddr = "127.0.0.1:8080"
	defaultEdgeTick = 2 * time.Second
)

func runEdge(_ Env, args []string) error {
	fs := flag.NewFlagSet("edge", flag.ContinueOnError)
	addr := fs.String("addr", defaultEdgeAddr, "address to listen on")
	routes := fs.String("routes", xdg.DeploysPath(), "deploys file to read routes from")
	every := fs.Duration("every", defaultEdgeTick, "how often to re-read the routes")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, edgeUsage, defaultEdgeAddr, xdg.DeploysPath(), defaultEdgeTick)
	}
	if err := fs.Parse(args); errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return err
	}

	store := edge.NewStore(*routes)
	srv := edge.NewServer(store)
	if err := store.Err(); err != nil {
		// Worth saying out loud: it will serve 404s for everything until the
		// file is readable again, and that is a confusing thing to debug.
		fmt.Fprintln(os.Stderr, "edge:", err)
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	fmt.Printf("loods edge: http://%s  (routes %s)\n", ln.Addr(), *routes)
	for _, r := range srv.Hosts() {
		fmt.Printf("  %s\n", describe(r))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan struct{})
	defer close(done)
	go srv.Watch(*every, done)

	server := &http.Server{
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	errs := make(chan error, 1)
	go func() {
		if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()
	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}
	shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return server.Shutdown(shut)
}

func describe(r edge.Route) string {
	switch {
	case r.Status != "":
		return fmt.Sprintf("%s  %s", r.Host, r.Status)
	case r.Dir != "":
		return fmt.Sprintf("%s  %s", r.Host, r.Dir)
	case r.Upstream != "":
		return fmt.Sprintf("%s  -> %s", r.Host, r.Upstream)
	}
	return r.Host + "  (nothing to serve)"
}
