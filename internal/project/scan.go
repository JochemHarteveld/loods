package project

import (
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"loods/internal/fsx"
	"loods/internal/gitx"
	"loods/internal/hygiene"
)

// result is one item's scan, kept apart from the item so the slow work can run
// on many items at once and be applied afterwards.
type result struct {
	size     int64
	lastFile time.Time
	stack    []string
	commands []Command
	git      *gitx.Status
	hygiene  hygiene.Report
}

// At most this many items are scanned at a time: a scan is mostly git
// processes and directory walks, and a board holds dozens of projects.
var sem = make(chan struct{}, 8)

// scan reads one item. withSize also walks noisy dirs (node_modules, build, …)
// to total the size; the board skips that because it is slow and unused there.
func scan(it *Item, withSize bool) result {
	sem <- struct{}{}
	defer func() { <-sem }()
	var r result
	dirStats(it.Path, false, withSize, &r.size, &r.lastFile)
	if it.IsProject() {
		r.stack = DetectStack(it.Path)
		r.commands = DetectCommands(it.Path)
	}
	if it.Kind == KindGit {
		st := gitx.Read(it.Path)
		r.git = &st
		r.hygiene = hygiene.Read(it.Path, st)
	}
	return r
}

// ScanAll fills in every item that has not been scanned yet, in parallel.
func ScanAll(items []*Item, withSize bool) {
	var wg sync.WaitGroup
	for _, it := range items {
		if it.Scanned {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			it.apply(scan(it, withSize))
		}()
	}
	wg.Wait()
}

func (it *Item) apply(r result) {
	it.SizeBytes, it.LastFile, it.Stack, it.Commands = r.size, r.lastFile, r.stack, r.commands
	if g := r.git; g != nil {
		it.LastCommit, it.Branch, it.Dirty = g.LastCommit, g.Branch, g.Dirty
		it.Unpushed, it.HasRemote, it.Stashes = g.Unpushed, g.HasRemote, g.Stashes
		it.Upstream, it.Ahead, it.Behind = g.Upstream, g.Ahead, g.Behind
		it.Branches, it.Worktrees, it.WebURL = g.Branches, g.Worktrees, g.WebURL
	}
	it.Scanned = true
	it.finalize()
	if r.git != nil {
		it.Warnings = r.hygiene.Warnings(it.Counts(), time.Now())
	}
}

// dirStats totals a folder's size and the newest change in it. Files in noisy
// dirs count toward the size but not toward "last touched".
func dirStats(dir string, noisy, withSize bool, size *int64, newest *time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.Type()&fs.ModeSymlink != 0 {
			continue
		}
		full := filepath.Join(dir, e.Name())
		if e.IsDir() {
			n := noisy || fsx.Noisy(e.Name())
			if n && !withSize {
				continue
			}
			dirStats(full, n, withSize, size, newest)
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		*size += info.Size()
		if !noisy && info.ModTime().After(*newest) {
			*newest = info.ModTime()
		}
	}
}
