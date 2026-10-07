package main

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
)

const logName = "graveyard.log"

// LogEntry is one line in <archive>/graveyard.log (JSON lines).
type LogEntry struct {
	Time   time.Time `json:"time"`
	Batch  string    `json:"batch"`
	Action string    `json:"action"` // archive | trash | restore
	From   string    `json:"from"`
	To     string    `json:"to,omitempty"`
	Risks  []string  `json:"risks,omitempty"`
}

func appendLog(archiveRoot string, e LogEntry) error {
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

func readLog(archiveRoot string) ([]LogEntry, error) {
	f, err := os.Open(filepath.Join(archiveRoot, logName))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []LogEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e LogEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	return out, sc.Err()
}

// archiveItem moves it to <archive>/<date>/<rel>. Rename only: it refuses to
// copy across filesystems so nothing is ever half-moved.
func archiveItem(archiveRoot string, it *Item, now time.Time) (string, error) {
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

func trashItem(it *Item) error {
	out, err := exec.Command("gio", "trash", it.Path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("gio trash: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// runUndo restores the last archived batch from the command line.
func runUndo(archiveRoot string) error {
	batch, results, err := undoArchive(archiveRoot)
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

// undoArchive restores the most recent archive batch that still has
// unrestored entries. An empty batch means there was nothing to undo.
func undoArchive(archiveRoot string) (string, []OpResult, error) {
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
	var results []OpResult
	for _, e := range entries {
		if e.Batch != batch || e.Action != "archive" || restored[e.To] {
			continue
		}
		r := OpResult{Name: e.From}
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
				appendLog(archiveRoot, LogEntry{Time: time.Now(), Batch: batch, Action: "restore", From: e.To, To: e.From})
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

// recentBatches lists the newest batches first.
func recentBatches(archiveRoot string, limit int) []Batch {
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
