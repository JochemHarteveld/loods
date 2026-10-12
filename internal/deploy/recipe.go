package deploy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"loods/internal/project"
)

// A Recipe is one deploy worked out: what to build, what to serve, and where.
// Resolving it is kept apart from doing it so the CLI can say what would
// happen, and so the server starts a process that is already decided.
type Recipe struct {
	Rel  string // project path relative to the root
	Dir  string // the project itself
	Host string
	Kind string

	Build string            // shell command, run in Dir; may be empty
	Run   string            // proc: the command to serve with, $PORT is set
	Env   map[string]string // both of them see this

	Serve string // static: the directory the build produces, absolute
	SPA   bool

	Service string // compose: the service to route to
	Port    int    // compose: its port inside the container
}

// Resolve works out how to publish one project: what deploys.yaml says, and
// what the project itself looks like where it says nothing. It refuses rather
// than guesses when a project does not look like anything it can publish —
// a wrong guess costs a build and a confusing hostname.
func Resolve(cfg Config, rel, dir string) (Recipe, error) {
	spec := cfg.Project(rel)
	r := Recipe{
		Rel:     rel,
		Dir:     dir,
		Kind:    spec.Kind,
		Build:   spec.Build,
		Run:     spec.Run,
		Env:     spec.Env,
		Service: spec.Service,
		Port:    spec.Port,
	}
	sub := spec.Sub
	if sub == "" {
		sub = rel
	}
	r.Host = Hostname(sub, cfg.Domain())
	if r.Host == "" {
		return r, fmt.Errorf("%s has no hostname: give it a `sub` in deploys.yaml", rel)
	}

	detected := detect(dir)
	if r.Kind == "" {
		r.Kind = detected.Kind
	}
	if r.Kind == "" {
		return r, fmt.Errorf("%s does not look like anything loods can publish; "+
			"say what it is in deploys.yaml (kind: static | proc | compose)", rel)
	}
	if r.Build == "" && r.Kind == detected.Kind {
		r.Build = detected.Build
	}

	switch r.Kind {
	case KindStatic:
		switch {
		case spec.Dir != "":
			r.Serve = abs(dir, spec.Dir)
		case detected.Serve != "":
			r.Serve = abs(dir, detected.Serve)
		default:
			return r, fmt.Errorf("%s: say which directory the build produces (dir: in deploys.yaml)", rel)
		}
		r.SPA = detected.SPA
		if spec.SPA != nil {
			r.SPA = *spec.SPA
		}
	case KindProc:
		if r.Run == "" {
			r.Run = detected.Run
		}
		if r.Run == "" {
			return r, fmt.Errorf("%s: say how to serve it (run: in deploys.yaml)", rel)
		}
	case KindCompose:
		if r.Service == "" || r.Port == 0 {
			return r, fmt.Errorf("%s: a compose deploy needs `service` and `port` in deploys.yaml", rel)
		}
		if r.Build == "" {
			r.Build = "docker compose up -d --build " + r.Service
		}
	default:
		return r, fmt.Errorf("%s: unknown kind %q", rel, r.Kind)
	}
	return r, nil
}

// Output is the directory a static deploy ended up producing. It is asked for
// after the build, not before: a project that has never been built does not
// have one yet, and that is not an error until the build is done.
func (r Recipe) Output() (string, error) {
	if r.Kind != KindStatic {
		return "", nil
	}
	if info, err := os.Stat(r.Serve); err != nil || !info.IsDir() {
		return "", fmt.Errorf("the build did not produce %s", r.Serve)
	}
	if _, err := os.Stat(filepath.Join(r.Serve, "index.html")); err != nil {
		return "", fmt.Errorf("%s has no index.html", r.Serve)
	}
	return r.Serve, nil
}

func abs(dir, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(dir, p)
}

// detect is the same idea as the garage's command detection, asked a different
// question: not "how do I run this while I work on it" but "how do I publish
// it". A dev script is deliberately not an answer.
func detect(dir string) Recipe {
	if r, ok := detectNode(dir); ok {
		return r
	}
	if exists(dir, "go.mod") && exists(dir, "main.go") {
		return Recipe{Kind: KindProc, Build: "", Run: "go run ."}
	}
	if exists(dir, "compose.yaml", "compose.yml", "docker-compose.yml", "docker-compose.yaml") {
		return Recipe{Kind: KindCompose}
	}
	return Recipe{}
}

func detectNode(dir string) (Recipe, bool) {
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return Recipe{}, false
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(b, &pkg) != nil {
		return Recipe{}, false
	}
	pm := project.PackageManager(dir)
	if pkg.Scripts["build"] != "" {
		r := Recipe{Kind: KindStatic, Build: pm + " run build", SPA: true}
		// Where the usual bundlers put it, most specific first.
		for _, out := range []string{"dist", "build", "out", "public"} {
			if info, err := os.Stat(filepath.Join(dir, out)); err == nil && info.IsDir() {
				r.Serve = out
				break
			}
		}
		if r.Serve == "" {
			r.Serve = "dist" // what it will probably be; the build decides
		}
		if pkg.Scripts["start"] != "" {
			// It builds *and* serves: that is a process, not a directory.
			return Recipe{Kind: KindProc, Build: pm + " run build", Run: pm + " start"}, true
		}
		return r, true
	}
	if pkg.Scripts["start"] != "" {
		return Recipe{Kind: KindProc, Run: pm + " start"}, true
	}
	return Recipe{}, false
}

func exists(dir string, names ...string) bool {
	for _, n := range names {
		if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
			return true
		}
	}
	return false
}
