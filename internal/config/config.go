// Package config is ~/.config/loods/config.yaml: the commands and stacks you
// add or override by hand. It is read on every scan, so an edit shows up within
// one rescan (press R).
package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"loods/internal/project"
)

type Config struct {
	Projects map[string]ProjectSpec `yaml:"projects"` // keyed by path relative to the root
	Stacks   map[string]StackSpec   `yaml:"stacks"`
	GitHub   *bool                  `yaml:"github"` // false: never call gh
}

// ProjectSpec is what you may say about one project: which command r starts,
// and commands to add, override or hide.
type ProjectSpec struct {
	Default  string                 `yaml:"default"`
	Commands map[string]CommandSpec `yaml:"commands"`
}

type CommandSpec struct {
	Run  string `yaml:"run"`
	Dir  string `yaml:"dir"`
	URL  string `yaml:"url"`
	Keys bool   `yaml:"keys"`
	Hide bool   `yaml:"hide"`
}

// StackSpec starts several things at once: one script, or a list of project
// commands.
type StackSpec struct {
	Dir      string   `yaml:"dir"`      // relative to the root
	Run      string   `yaml:"run"`      // one script that starts everything …
	Commands []string `yaml:"commands"` // … or project commands: "group/project:command"
}

// GitHubEnabled reports whether loods may ask gh about this board.
func (c Config) GitHubEnabled() bool { return c.GitHub == nil || *c.GitHub }

const template = `# loods config. Read on every scan: save, then press R in loods.
#
# projects:
#   transpaclean/transpaclean-flutterapp:     # path relative to ~/Projects
#     default: app flutter                     # what r starts without asking
#     commands:
#       app flutter: { run: flutter run -d linux }   # override a detected command
#       dashboard dev: { hide: true }                # hide one
#       web: { run: npm run dev, dir: web, url: http://localhost:5173 }  # add one
#
# stacks:
#   transpaclean:                              # one script …
#     dir: transpaclean
#     run: ./dev-stack.sh --no-fw
#   site:                                      # … or several project commands
#     commands:
#       - blauweschuit/de-website-en-backend:compose
#       - blauweschuit/de-website-en-backend:dev
#
# github: false     # don't ask gh for PRs, issues and CI (on by default when gh is logged in)
`

// Load reads the config, writing a commented template on first run.
func Load(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		if os.MkdirAll(filepath.Dir(path), 0o755) == nil {
			os.WriteFile(path, []byte(template), 0o644)
		}
		return c, nil
	}
	if err != nil {
		return c, err
	}
	err = yaml.Unmarshal(b, &c)
	return c, err
}

// Apply merges configured commands into the detected ones and picks each
// project's default command.
func (c Config) Apply(items []*project.Item) {
	for _, it := range items {
		pc := c.Projects[it.Rel]
		for _, name := range sortedKeys(pc.Commands) {
			apply(it, name, pc.Commands[name])
		}
		it.DefaultCommand = ""
		if slices.ContainsFunc(it.Commands, func(c project.Command) bool { return c.Name == pc.Default }) {
			it.DefaultCommand = pc.Default
		} else if len(it.Commands) > 0 {
			it.DefaultCommand = it.Commands[0].Name
		}
	}
}

// apply is one configured command against one project: hide a detected one,
// override its fields, or add a command of your own.
func apply(it *project.Item, name string, cc CommandSpec) {
	i := slices.IndexFunc(it.Commands, func(c project.Command) bool { return c.Name == name })
	switch {
	case cc.Hide:
		if i >= 0 {
			it.Commands = slices.Delete(it.Commands, i, i+1)
		}
	case i >= 0:
		cmd := &it.Commands[i]
		if cc.Run != "" {
			cmd.Run, cmd.Source = cc.Run, "config"
		}
		if cc.Dir != "" {
			cmd.Dir = cc.Dir
		}
		if cc.URL != "" {
			cmd.URL = cc.URL
		}
		cmd.Keys = cmd.Keys || cc.Keys
	case cc.Run != "":
		it.Commands = append(it.Commands, project.Command{
			Name: name, Run: cc.Run, Dir: cc.Dir, URL: cc.URL, Keys: cc.Keys, Source: "config",
		})
	}
}

// Stack is a configured or detected stack as the board shows it.
type Stack struct {
	Name     string   `json:"name"`
	Dir      string   `json:"dir"`
	Run      string   `json:"run,omitempty"`
	Commands []string `json:"commands,omitempty"`
	Source   string   `json:"source"`
}

// FindStacks returns the configured stacks plus an executable dev-stack.sh in
// any group folder (like ~/Projects/transpaclean).
func (c Config) FindStacks(root string, items []*project.Item) []Stack {
	var out []Stack
	seen := map[string]bool{}
	for _, n := range sortedKeys(c.Stacks) {
		sc := c.Stacks[n]
		dir := filepath.Join(root, sc.Dir)
		out = append(out, Stack{Name: n, Dir: dir, Run: sc.Run, Commands: sc.Commands, Source: "config"})
		seen[dir+"|"+strings.TrimSpace(sc.Run)] = true
	}
	groups := map[string]bool{}
	for _, it := range items {
		if it.Group == "" || groups[it.Group] {
			continue
		}
		groups[it.Group] = true
		dir := filepath.Join(root, it.Group)
		info, err := os.Stat(filepath.Join(dir, "dev-stack.sh"))
		if err != nil || info.Mode()&0o111 == 0 || seen[dir+"|./dev-stack.sh"] {
			continue
		}
		out = append(out, Stack{Name: it.Group, Dir: dir, Run: "./dev-stack.sh", Source: "dev-stack.sh"})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// sortedKeys keeps the order a config is applied in stable, whatever order
// YAML handed the map over in.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
