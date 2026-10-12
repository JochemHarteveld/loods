package cli

import (
	"errors"
	"fmt"

	"loods/internal/skills"
)

// runClaude installs or removes what loods adds to Claude Code: the /wrapup,
// /todo and /orchestrate skills and the SessionStart hook.
func runClaude(_ Env, args []string) error {
	if len(args) != 1 || (args[0] != "install" && args[0] != "uninstall") {
		return errors.New("usage: loods claude install|uninstall\n\n" +
			"install adds the /wrapup, /todo and /orchestrate skills and a SessionStart hook that shows Claude the plan")
	}
	act := skills.Install
	if args[0] == "uninstall" {
		act = skills.Uninstall
	}
	did, err := act()
	for _, line := range did {
		fmt.Println(line)
	}
	return err
}
