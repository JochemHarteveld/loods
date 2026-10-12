package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"loods/internal/claim"
	"loods/internal/plan"
	"loods/internal/project"
	"loods/internal/xdg"
)

// runHook is a Claude Code SessionStart hook: what it prints becomes context
// for the new session, so Claude starts knowing where the project stands.
// It never fails the session.
func runHook(env Env, _ []string) error {
	var in struct {
		Cwd string `json:"cwd"`
	}
	if json.NewDecoder(io.LimitReader(os.Stdin, 1<<20)).Decode(&in) != nil || in.Cwd == "" {
		return nil
	}
	it := project.ContainingWorktree(project.Board(env.Root, env.Depth, env.Archive), in.Cwd)
	if it == nil {
		return nil
	}
	plans := plan.NewStore(xdg.PlansPath())
	plans.Refresh()
	p := plans.Get(it.Rel)
	if p.Empty() {
		fmt.Printf("This project (%s) is on the loods board without a plan yet. When the user wraps up, /wrapup records its status, next step and tasks.\n", it.Rel)
		return nil
	}
	fmt.Println("Where this project stands, from the loods board (`loods plan`):")
	fmt.Println()
	var b bytes.Buffer
	printPlan(&b, it.Rel, p)
	fmt.Print(b.String())
	fmt.Println()

	// Task numbers are stable, so the user can say "do todo #4" and mean it.
	claims := claim.NewStore(xdg.ClaimsPath())
	claims.Refresh()
	if open := p.OpenTasks(); len(open) > 0 {
		fmt.Printf("Open tasks you can be asked to pick up, by number (%s#<n>):\n", it.Rel)
		for _, t := range open {
			line := fmt.Sprintf("  #%-3d %s", t.ID, t.Text)
			if t.State == "doing" {
				line += "  (in progress)"
			}
			if c, ok := claims.Get(it.Rel, t.ID); ok {
				state := "working on it"
				if c.Stale {
					state = "stalled, no heartbeat"
				}
				line += fmt.Sprintf("  ← %s %s", c.Agent, state)
			}
			fmt.Println(line)
		}
		fmt.Println()
		fmt.Println("Asked to do one? Claim it first: `loods todo claim " + it.Rel + "#<n>` (see /todo).")
	}
	fmt.Println("Keep it current: when the user wraps up, run /wrapup (or `loods plan next|note|task|doing|done`).")
	return nil
}
