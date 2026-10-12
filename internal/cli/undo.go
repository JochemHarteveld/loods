package cli

import (
	"fmt"

	"loods/internal/graveyard"
)

// runUndo restores the last archived batch from the command line. Trashed
// items are the file manager's business, not ours.
func runUndo(env Env, _ []string) error {
	batch, results, err := graveyard.Undo(env.Archive)
	if err != nil {
		return err
	}
	if batch == "" {
		fmt.Println("nothing to undo (trashed items: restore them from your file manager's trash)")
		return nil
	}
	fmt.Printf("restored batch %s\n", batch)
	for _, r := range results {
		if r.OK {
			fmt.Printf("  ok    %s\n", r.Name)
		} else {
			fmt.Printf("  fail  %s: %s\n", r.Name, r.Error)
		}
	}
	return nil
}
