// Package xdg resolves every file loods reads or writes outside a project.
//
// Two kinds of file, kept apart on purpose: the config dir holds what you edit
// by hand (config.yaml) and what loods rewrites but you may still read
// (plans.yaml); the state dir holds what loods remembers between runs and you
// never edit (surviving processes, task claims, runs, the cleanup log).
package xdg

import (
	"os"
	"path/filepath"
)

// ConfigPath is the hand-written config: ~/.config/loods/config.yaml.
func ConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "loods", "config.yaml")
}

// PlansPath sits next to the config: loods rewrites it, you can still read it.
func PlansPath() string { return filepath.Join(filepath.Dir(ConfigPath()), "plans.yaml") }

// StateDir holds what loods remembers between runs but you never edit.
func StateDir() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "loods")
}

func StateFile(name string) string { return filepath.Join(StateDir(), name) }

// WorktreesDir holds the git worktrees of isolated run jobs, one folder per
// project. It is outside every project on purpose: a worktree under a project
// folder would be scanned as a project of its own.
func WorktreesDir() string { return StateFile("worktrees") }

func ProcsPath() string  { return StateFile("procs.json") }
func ClaimsPath() string { return StateFile("claims.json") }
func RunsPath() string   { return StateFile("runs.json") }

// DeploysPath is what is published and where it is routed. loods edge reads
// it, possibly in another process or on another machine, so it is a document
// of its own rather than part of the board's memory.
func DeploysPath() string { return StateFile("deploys.json") }

// DeploysConfigPath is the hand-written half of deploying: what each project
// publishes and how it is built.
func DeploysConfigPath() string { return filepath.Join(filepath.Dir(ConfigPath()), "deploys.yaml") }

// ClaudeDir is Claude Code's own config dir, which holds the skills loods
// installs and the transcripts it reads.
func ClaudeDir() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

func ClaudeProjectsDir() string { return filepath.Join(ClaudeDir(), "projects") }
