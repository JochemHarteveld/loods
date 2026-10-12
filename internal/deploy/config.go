package deploy

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config is ~/.config/loods/deploys.yaml: the domain, and what each project
// publishes. Everything in it is optional — a project with no entry at all is
// detected like a dev server is — so the common case is an empty file and a
// `loods deploy` in the project.
type Config struct {
	Site    Site                   `yaml:"site"`
	Deploys map[string]ProjectSpec `yaml:"deploys"` // keyed by path relative to the root
}

type Site struct {
	Domain string `yaml:"domain"` // loods.site
	Edge   string `yaml:"edge"`   // where loods edge listens, for the record
}

// ProjectSpec is what you may say about one deploy. An empty field means "work
// it out", not "do nothing".
type ProjectSpec struct {
	Sub     string            `yaml:"sub"`     // subdomain; default the project folder name
	Kind    string            `yaml:"kind"`    // static | proc | compose
	Build   string            `yaml:"build"`   // shell command, run in the project
	Dir     string            `yaml:"dir"`     // static: what the build produced
	SPA     *bool             `yaml:"spa"`     // static: unknown paths get index.html
	Run     string            `yaml:"run"`     // proc: the command to serve with, $PORT is set
	Env     map[string]string `yaml:"env"`     // proc: environment for build and run
	Service string            `yaml:"service"` // compose: the service to route to
	Port    int               `yaml:"port"`    // compose: its container port
	Public  bool              `yaml:"public"`  // phase 3: no Access in front of it
}

const DefaultDomain = "loods.site"

func (c Config) Domain() string {
	if c.Site.Domain != "" {
		return c.Site.Domain
	}
	return DefaultDomain
}

func (c Config) Project(rel string) ProjectSpec { return c.Deploys[rel] }

const template = `# What loods publishes, and where. Read on every deploy.
#
# Everything here is optional: a project with no entry is detected the same way
# its dev command is, and published at <project>.` + DefaultDomain + `.
#
# site:
#   domain: ` + DefaultDomain + `
#
# deploys:
#   bierreel:                    # path relative to ~/Projects
#     kind: static               # a built directory, served by loods edge
#     build: npm run build
#     dir: build                 # what the build produced
#     spa: true                  # unknown paths get index.html
#
#   transpaclean/TranspaBridge:
#     sub: bridge                # bridge.` + DefaultDomain + ` instead of transpabridge.
#     kind: proc                 # a long-running command on a port of its own
#     build: bun run build
#     run: bun run start         # $PORT is set for it
#     env:
#       NODE_ENV: production
#
#   blauweschuit/de-website-en-backend:
#     kind: compose
#     service: web               # the service to route to …
#     port: 80                   # … and the port it listens on inside
#
# Secrets belong in env here (this file is yours, the repository is not), or in
# a file outside the working tree that the run command reads.
`

// LoadConfig reads the config, writing a commented template on first run so
// the file explains itself before anyone needs it.
func LoadConfig(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		if os.MkdirAll(filepath.Dir(path), 0o755) == nil {
			os.WriteFile(path, []byte(template), 0o600)
		}
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		return c, err
	}
	return c, nil
}
