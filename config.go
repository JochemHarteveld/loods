package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is ~/.config/loods/config.yaml. It is read on every scan, so edits
// show up within one rescan (press R).
type Config struct {
	Projects map[string]ProjectConfig `yaml:"projects"` // keyed by path relative to the root
	Stacks   map[string]StackConfig   `yaml:"stacks"`
	GitHub   *bool                    `yaml:"github"` // false: never call gh
}

type ProjectConfig struct {
	Default  string                   `yaml:"default"`
	Commands map[string]CommandConfig `yaml:"commands"`
}

type CommandConfig struct {
	Run  string `yaml:"run"`
	Dir  string `yaml:"dir"`
	URL  string `yaml:"url"`
	Keys bool   `yaml:"keys"`
	Hide bool   `yaml:"hide"`
}

type StackConfig struct {
	Dir      string   `yaml:"dir"`      // relative to the root
	Run      string   `yaml:"run"`      // one script that starts everything …
	Commands []string `yaml:"commands"` // … or project commands: "group/project:command"
}

// Stack starts several things at once: one script, or a list of project commands.
type Stack struct {
	Name     string   `json:"name"`
	Dir      string   `json:"dir"`
	Run      string   `json:"run,omitempty"`
	Commands []string `json:"commands,omitempty"`
	Source   string   `json:"source"`
}

func configPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "loods", "config.yaml")
}

const configTemplate = `# loods config. Read on every scan: save, then press R in loods.
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

// loadConfig reads the config, writing a commented template on first run.
func loadConfig(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		if os.MkdirAll(filepath.Dir(path), 0o755) == nil {
			os.WriteFile(path, []byte(configTemplate), 0o644)
		}
		return c, nil
	}
	if err != nil {
		return c, err
	}
	err = yaml.Unmarshal(b, &c)
	return c, err
}

// applyConfig merges config commands into the detected ones and picks each
// project's default command.
func applyConfig(items []*Item, c Config) {
	for _, it := range items {
		pc := c.Projects[it.Rel]
		names := make([]string, 0, len(pc.Commands))
		for n := range pc.Commands {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			cc := pc.Commands[n]
			i := slices.IndexFunc(it.Commands, func(c Command) bool { return c.Name == n })
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
				it.Commands = append(it.Commands, Command{Name: n, Run: cc.Run, Dir: cc.Dir, URL: cc.URL, Keys: cc.Keys, Source: "config"})
			}
		}
		it.DefaultCommand = ""
		if slices.ContainsFunc(it.Commands, func(c Command) bool { return c.Name == pc.Default }) {
			it.DefaultCommand = pc.Default
		} else if len(it.Commands) > 0 {
			it.DefaultCommand = it.Commands[0].Name
		}
	}
}

// findStacks returns configured stacks plus an executable dev-stack.sh in any
// group folder (like ~/Projects/transpaclean).
func findStacks(root string, items []*Item, c Config) []Stack {
	var out []Stack
	seen := map[string]bool{}
	names := make([]string, 0, len(c.Stacks))
	for n := range c.Stacks {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		sc := c.Stacks[n]
		out = append(out, Stack{Name: n, Dir: filepath.Join(root, sc.Dir), Run: sc.Run, Commands: sc.Commands, Source: "config"})
		seen[filepath.Join(root, sc.Dir)+"|"+strings.TrimSpace(sc.Run)] = true
	}
	groups := map[string]bool{}
	for _, it := range items {
		if it.Group != "" && !groups[it.Group] {
			groups[it.Group] = true
			dir := filepath.Join(root, it.Group)
			info, err := os.Stat(filepath.Join(dir, "dev-stack.sh"))
			if err != nil || info.Mode()&0o111 == 0 || seen[dir+"|./dev-stack.sh"] {
				continue
			}
			out = append(out, Stack{Name: it.Group, Dir: dir, Run: "./dev-stack.sh", Source: "dev-stack.sh"})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
