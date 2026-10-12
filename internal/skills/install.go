package skills

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"loods/internal/xdg"
)

// Install writes the skills and adds the SessionStart hook, and returns a line
// per thing it did, for the command that asked. It refuses rather than
// overwrite a skill file loods did not write.
func Install() ([]string, error) {
	for _, name := range Names() {
		path := Path(name)
		if b, err := os.ReadFile(path); err == nil && !bytes.Contains(b, []byte(Marker)) {
			return nil, fmt.Errorf("%s exists and was not written by loods; not touching it", path)
		}
	}
	var did []string
	for _, name := range Names() {
		path := Path(name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return did, err
		}
		if err := os.WriteFile(path, []byte(files[name]), 0o644); err != nil {
			return did, err
		}
		did = append(did, "wrote "+path)
	}
	settings := SettingsPath()
	changed, err := EditHooks(settings, true)
	if err != nil {
		return did, err
	}
	if changed {
		did = append(did, fmt.Sprintf("added a SessionStart hook (%s) to %s\n  backup: %s.loods-backup", HookCommand(), settings, settings))
	} else {
		did = append(did, "SessionStart hook already in "+settings)
	}
	return did, nil
}

// Uninstall removes the skills loods wrote and its hook, and leaves everything
// else alone.
func Uninstall() ([]string, error) {
	var did []string
	for _, name := range Names() {
		path := Path(name)
		if b, err := os.ReadFile(path); err == nil && bytes.Contains(b, []byte(Marker)) {
			os.Remove(path)
			os.Remove(filepath.Dir(path))
			did = append(did, "removed "+path)
		}
	}
	settings := SettingsPath()
	changed, err := EditHooks(settings, false)
	if err == nil && changed {
		did = append(did, "removed the SessionStart hook from "+settings)
	}
	return did, err
}

// Path is where one skill lives in Claude Code's config.
func Path(name string) string {
	return filepath.Join(xdg.ClaudeDir(), "skills", name, "SKILL.md")
}

// SettingsPath is the settings.json the hook goes into.
func SettingsPath() string { return filepath.Join(xdg.ClaudeDir(), "settings.json") }

// HookCommand is the command line the hook runs: this binary, if it can find
// itself, so a loods that is not on PATH still works.
func HookCommand() string {
	exe := "loods"
	if p, err := exec.LookPath("loods"); err == nil {
		exe = p
	} else if p, err := os.Executable(); err == nil {
		exe = p
	}
	return exe + " hook"
}

// isOurs reports whether a hook entry in settings.json is the one loods added.
func isOurs(e json.RawMessage) bool {
	var h struct {
		Hooks []struct {
			Command string `json:"command"`
		} `json:"hooks"`
	}
	if json.Unmarshal(e, &h) != nil {
		return false
	}
	for _, c := range h.Hooks {
		if strings.HasSuffix(c.Command, "loods hook") {
			return true
		}
	}
	return false
}

// EditHooks adds or removes our SessionStart hook in settings.json and reports
// whether the file changed. Only the "hooks" value is rebuilt; every other key
// keeps its place and formatting, and the original is kept as a backup.
func EditHooks(path string, add bool) (bool, error) {
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	orig, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if !add {
			return false, nil
		}
		orig = []byte("{}")
	} else if err != nil {
		return false, err
	}
	keys, vals, err := topLevel(orig)
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	hooks := map[string][]json.RawMessage{}
	i := slices.Index(keys, "hooks")
	if i >= 0 {
		if err := json.Unmarshal(vals[i], &hooks); err != nil {
			return false, fmt.Errorf("%s: hooks: %w", path, err)
		}
	}
	start := slices.DeleteFunc(slices.Clone(hooks["SessionStart"]), isOurs)
	had := len(start) != len(hooks["SessionStart"])
	if add == had {
		return false, nil
	}
	if add {
		entry, _ := json.Marshal(map[string]any{"hooks": []map[string]any{{"type": "command", "command": HookCommand(), "timeout": 10}}})
		start = append(start, entry)
	}
	if len(start) > 0 {
		hooks["SessionStart"] = start
	} else {
		delete(hooks, "SessionStart")
	}
	hv, _ := json.Marshal(hooks)
	switch {
	case i >= 0 && len(hooks) == 0:
		keys, vals = slices.Delete(keys, i, i+1), slices.Delete(vals, i, i+1)
	case i >= 0:
		vals[i] = hv
	default:
		keys, vals = append(keys, "hooks"), append(vals, hv)
	}
	var out bytes.Buffer
	out.WriteString("{")
	for j, k := range keys {
		if j > 0 {
			out.WriteString(",")
		}
		kb, _ := json.Marshal(k)
		out.Write(kb)
		out.WriteString(":")
		out.Write(vals[j])
	}
	out.WriteString("}")
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, out.Bytes(), "", "  "); err != nil {
		return false, err
	}
	pretty.WriteString("\n")
	if err := os.WriteFile(path+".loods-backup", orig, 0o600); err != nil {
		return false, err
	}
	tmp := path + ".loods-tmp"
	if err := os.WriteFile(tmp, pretty.Bytes(), mode); err != nil {
		return false, err
	}
	return true, os.Rename(tmp, path)
}

// topLevel splits a JSON object into its keys and raw values, in order.
func topLevel(b []byte) ([]string, []json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, nil, errors.New("not a JSON object")
	}
	var keys []string
	var vals []json.RawMessage
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, nil, err
		}
		keys, vals = append(keys, t.(string)), append(vals, v)
	}
	return keys, vals, nil
}
