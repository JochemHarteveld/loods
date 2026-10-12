// Package graveyard moves dead projects out of the way: archive them to
// <archive>/<date>/<rel>, or hand them to the desktop trash.
//
// Every move is one line in <archive>/graveyard.log, so a whole batch can be
// put back. Archiving is a rename and nothing else — it refuses to copy across
// filesystems, so nothing is ever half-moved.
package graveyard

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"loods/internal/ops"
	"loods/internal/project"
)

const logName = "graveyard.log"

// Entry is one line in <archive>/graveyard.log (JSON lines).
type Entry struct {
	Time   time.Time `json:"time"`
	Batch  string    `json:"batch"`
	Action string    `json:"action"` // archive | trash | restore
	From   string    `json:"from"`
	To     string    `json:"to,omitempty"`
	Risks  []string  `json:"risks,omitempty"`
}

// AppendLog records one move, so Undo can find it again.
func AppendLog(archiveRoot string, e Entry) error {
	if err := os.MkdirAll(archiveRoot, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(archiveRoot, logName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	b, _ := json.Marshal(e)
	_, err = f.Write(append(b, '\n'))
	return err
}

func readLog(archiveRoot string) ([]Entry, error) {
	f, err := os.Open(filepath.Join(archiveRoot, logName))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Entry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	return out, sc.Err()
}

// Archive moves a project to <archive>/<date>/<rel> and returns where it went.
func Archive(archiveRoot string, it *project.Item, now time.Time) (string, error) {
	dest := filepath.Join(archiveRoot, now.Format("2006-01-02"), it.Rel)
	if _, err := os.Lstat(dest); err == nil {
		dest += "-" + now.Format("150405")
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(it.Path, dest); err != nil {
		if errors.Is(err, syscall.EXDEV) {
			return "", fmt.Errorf("archive is on another filesystem; not moved")
		}
		return "", err
	}
	return dest, nil
}

// Trash hands a project to the desktop trash, where the file manager can
// restore it.
func Trash(it *project.Item) error {
	out, err := exec.Command("gio", "trash", it.Path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("gio trash: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// Undo restores the most recent archive batch that still has unrestored
// entries. An empty batch name means there was nothing to undo.
func Undo(archiveRoot string) (string, []ops.Result, error) {
	entries, err := readLog(archiveRoot)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	restored := map[string]bool{}
	for _, e := range entries {
		if e.Action == "restore" {
			restored[e.From] = true
		}
	}
	var batch string
	for i := len(entries) - 1; i >= 0; i-- {
		if e := entries[i]; e.Action == "archive" && !restored[e.To] {
			batch = e.Batch
			break
		}
	}
	if batch == "" {
		return "", nil, nil
	}
	var results []ops.Result
	for _, e := range entries {
		if e.Batch != batch || e.Action != "archive" || restored[e.To] {
			continue
		}
		r := ops.Result{Name: e.From}
		switch _, err := os.Lstat(e.From); {
		case err == nil:
			r.Error = "something already exists there"
		case os.MkdirAll(filepath.Dir(e.From), 0o755) != nil:
			r.Error = "cannot create the parent folder"
		default:
			if err := os.Rename(e.To, e.From); err != nil {
				r.Error = err.Error()
			} else {
				r.OK = true
				AppendLog(archiveRoot, Entry{Time: time.Now(), Batch: batch, Action: "restore", From: e.To, To: e.From})
			}
		}
		results = append(results, r)
	}
	return batch, results, nil
}

// Batch summarises one archive or trash run from graveyard.log.
type Batch struct {
	Batch    string    `json:"batch"`
	Time     time.Time `json:"time"`
	Action   string    `json:"action"` // archive | trash
	Items    []string  `json:"items"`  // paths as they were
	Restored bool      `json:"restored,omitempty"`
}

// RecentBatches lists the newest batches first, for the Graveyard's undo list.
func RecentBatches(archiveRoot string, limit int) []Batch {
	entries, _ := readLog(archiveRoot)
	restored := map[string]bool{}
	for _, e := range entries {
		if e.Action == "restore" {
			restored[e.Batch] = true
		}
	}
	var out []Batch
	index := map[string]int{}
	for _, e := range entries {
		if e.Action != "archive" && e.Action != "trash" {
			continue
		}
		i, ok := index[e.Batch]
		if !ok {
			i = len(out)
			index[e.Batch] = i
			out = append(out, Batch{Batch: e.Batch, Time: e.Time, Action: e.Action, Restored: restored[e.Batch]})
		}
		out[i].Items = append(out[i].Items, e.From)
	}
	slices.Reverse(out)
	return out[:min(len(out), limit)]
}
