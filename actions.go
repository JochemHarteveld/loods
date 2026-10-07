package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// runUndo restores the most recent archive batch that still has unrestored entries.
func runUndo(archiveRoot string) error {
	entries, err := readLog(archiveRoot)
	if err != nil {
		return fmt.Errorf("no log at %s: %w", filepath.Join(archiveRoot, logName), err)
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
		fmt.Println("nothing to undo (trashed items: restore them from your file manager's trash)")
		return nil
	}
	fmt.Printf("restoring batch %s\n", batch)
	for _, e := range entries {
		if e.Batch != batch || e.Action != "archive" || restored[e.To] {
			continue
		}
		if _, err := os.Lstat(e.From); err == nil {
			fmt.Printf("  skip  %s (something already exists there)\n", e.From)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(e.From), 0o755); err != nil {
			fmt.Printf("  fail  %s: %v\n", e.From, err)
			continue
		}
		if err := os.Rename(e.To, e.From); err != nil {
			fmt.Printf("  fail  %s: %v\n", e.From, err)
			continue
		}
		appendLog(archiveRoot, LogEntry{Time: time.Now(), Batch: batch, Action: "restore", From: e.To, To: e.From})
		fmt.Printf("  ok    %s\n", e.From)
	}
	return nil
}
