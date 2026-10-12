// Package ops carries the outcome of one item of a batch action (archive a
// project, delete a branch, restore a worktree) back to the board, which shows
// a line per item whether it worked or not.
package ops

type Result struct {
	Name  string `json:"name"`
	OK    bool   `json:"ok"`
	Note  string `json:"note,omitempty"`
	Error string `json:"error,omitempty"`
}
