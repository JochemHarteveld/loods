package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"loods/internal/project"
)

// locate resolves -p, or the project containing the working directory. It
// discovers the board itself: the commands work with or without a server.
func locate(env Env, rel string) (*project.Item, error) {
	items := project.Board(env.Root, env.Depth, env.Archive)
	if rel != "" {
		rel = strings.Trim(rel, "/")
		for _, it := range items {
			if it.Rel == rel {
				return it, nil
			}
		}
		return nil, fmt.Errorf("no project %q under %s", rel, env.Root)
	}
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	if it := project.ContainingWorktree(items, wd); it != nil {
		return it, nil
	}
	return nil, fmt.Errorf("%s is not inside a project under %s (use -p group/project)", wd, env.Root)
}

// taskRef reads "group/project#4", "#4" or "4": the way you address one task
// of one project everywhere in loods.
func taskRef(env Env, ref, pflag string) (*project.Item, int, error) {
	rel, num := "", ref
	if i := strings.LastIndex(ref, "#"); i >= 0 {
		rel, num = strings.Trim(ref[:i], "/"), ref[i+1:]
	}
	id, err := strconv.Atoi(strings.TrimSpace(num))
	if err != nil || id <= 0 {
		return nil, 0, fmt.Errorf("%q is not a task number; use loods#4, #4 or 4", ref)
	}
	if rel == "" {
		rel = pflag
	}
	it, err := locate(env, rel)
	if err != nil {
		return nil, 0, err
	}
	return it, id, nil
}

// writeJSON is the --json output of every command: indented, one value.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// author is who an edit is recorded as: Claude says so itself through the
// environment it runs its tools in.
func author() string {
	if os.Getenv("CLAUDECODE") != "" {
		return "claude"
	}
	return "you"
}

// first is the first of two values that is set, for "flag, else environment".
func first(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
