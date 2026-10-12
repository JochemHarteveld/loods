package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditHooksKeepsOtherSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	orig := `{
  "model": "opus",
  "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "notify"}]}]},
  "permissions": {"allow": ["Bash(ls:*)"]}
}
`
	os.WriteFile(path, []byte(orig), 0o640)
	if changed, err := EditHooks(path, true); err != nil || !changed {
		t.Fatal(changed, err)
	}
	if changed, _ := EditHooks(path, true); changed {
		t.Error("second install changed the file again")
	}
	b, _ := os.ReadFile(path)
	s := string(b)
	if !(strings.Index(s, `"model"`) < strings.Index(s, `"hooks"`) && strings.Index(s, `"hooks"`) < strings.Index(s, `"permissions"`)) {
		t.Errorf("key order changed:\n%s", s)
	}
	if !strings.Contains(s, `"notify"`) || !strings.Contains(s, `loods hook"`) || !strings.Contains(s, "SessionStart") {
		t.Errorf("hooks wrong:\n%s", s)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v", info.Mode().Perm())
	}
	if changed, err := EditHooks(path, false); err != nil || !changed {
		t.Fatal(changed, err)
	}
	b, _ = os.ReadFile(path)
	if strings.Contains(string(b), "SessionStart") || !strings.Contains(string(b), `"notify"`) {
		t.Errorf("uninstall left:\n%s", b)
	}
}
