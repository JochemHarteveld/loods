package main

import (
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Kind string

const (
	KindGit    Kind = "git"  // has .git
	KindProj   Kind = "proj" // has a project manifest but no git
	KindFolder Kind = "dir"  // plain folder
	KindArc    Kind = "zip"  // archive file
)

var projectMarkers = []string{
	"package.json", "pubspec.yaml", "go.mod", "Cargo.toml", "pyproject.toml",
	"requirements.txt", "composer.json", "pom.xml", "build.gradle", "Makefile",
	"docker-compose.yml", "compose.yml", "CMakeLists.txt", "platformio.ini",
}

var archiveExts = []string{".tar.gz", ".tgz", ".zip", ".tar", ".7z", ".rar"}

// Dirs whose files count toward size but not toward "last touched".
var noisyDirs = map[string]bool{
	".git": true, "node_modules": true, "build": true, ".dart_tool": true,
	"target": true, ".venv": true, "venv": true, "dist": true, ".svelte-kit": true,
	".next": true, "__pycache__": true, ".gradle": true, "Pods": true,
	"vendor": true, ".idea": true, ".pio": true,
}

type Item struct {
	Path       string    `json:"path"`
	Rel        string    `json:"rel"`
	Name       string    `json:"name"`
	Group      string    `json:"group"` // first folder of Rel for nested projects, "" for top level
	Kind       Kind      `json:"kind"`
	Stack      []string  `json:"stack,omitempty"`
	SizeBytes  int64     `json:"size_bytes"`
	Activity   time.Time `json:"last_activity"`
	LastFile   time.Time `json:"last_file_change"`
	LastCommit time.Time `json:"last_commit,omitzero"`
	Branch     string    `json:"branch,omitempty"`
	Dirty      int       `json:"dirty_files,omitempty"`
	Unpushed   int       `json:"unpushed_commits,omitempty"`
	HasRemote  bool      `json:"has_remote,omitempty"`
	Stashes    int       `json:"stashes,omitempty"`
	Upstream   string    `json:"upstream,omitempty"` // of the current branch
	Ahead      int       `json:"ahead,omitempty"`    // current branch vs its upstream (as of last fetch)
	Behind     int       `json:"behind,omitempty"`
	Branches   int       `json:"branches,omitempty"`
	Worktrees  int       `json:"worktrees,omitempty"` // linked worktrees, not counting the main checkout
	WebURL     string    `json:"web_url,omitempty"`
	Commands   []Command `json:"commands,omitempty"`
	// DefaultCommand is what r starts without asking (config default, else the first).
	DefaultCommand string    `json:"default_command,omitempty"`
	Risks          []string  `json:"risks,omitempty"`
	Warnings       []Warning `json:"warnings,omitempty"`
	Dupes          []string  `json:"possible_duplicates,omitempty"`

	Scanned bool `json:"-"`
	Gone    bool `json:"-"`
}

// Discover finds candidate items under root. Folders that only group other
// projects (like ~/Projects/transpaclean) are descended into, not listed.
func Discover(root string, maxDepth int, skip string) []*Item {
	var out []*Item
	walk(root, root, 0, maxDepth, skip, &out)
	sort.Slice(out, func(a, b int) bool { return out[a].Rel < out[b].Rel })
	return out
}

func walk(root, dir string, depth, maxDepth int, skip string, out *[]*Item) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || e.Type()&fs.ModeSymlink != 0 {
			continue
		}
		full := filepath.Join(dir, name)
		if full == skip {
			continue
		}
		if !e.IsDir() {
			if isArchiveFile(name) {
				if info, err := e.Info(); err == nil {
					it := newItem(root, full, KindArc)
					it.SizeBytes, it.LastFile, it.Scanned = info.Size(), info.ModTime(), true
					it.finalize()
					*out = append(*out, it)
				}
			}
			continue
		}
		if k := classify(full); k != "" {
			*out = append(*out, newItem(root, full, k))
			continue
		}
		if depth+1 < maxDepth && containsProjects(full, maxDepth-depth-1) {
			walk(root, full, depth+1, maxDepth, skip, out)
			continue
		}
		*out = append(*out, newItem(root, full, KindFolder))
	}
}

func newItem(root, path string, k Kind) *Item {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = path
	}
	group := ""
	if i := strings.IndexByte(rel, filepath.Separator); i > 0 {
		group = rel[:i]
	}
	return &Item{Path: path, Rel: rel, Name: filepath.Base(rel), Group: group, Kind: k}
}

func classify(dir string) Kind {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return KindGit
	}
	for _, m := range projectMarkers {
		if _, err := os.Stat(filepath.Join(dir, m)); err == nil {
			return KindProj
		}
	}
	return ""
}

func containsProjects(dir string, levels int) bool {
	if levels <= 0 {
		return false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") || e.Type()&fs.ModeSymlink != 0 {
			continue
		}
		if !e.IsDir() {
			if isArchiveFile(e.Name()) {
				return true
			}
			continue
		}
		full := filepath.Join(dir, e.Name())
		if classify(full) != "" || containsProjects(full, levels-1) {
			return true
		}
	}
	return false
}

func isArchiveFile(name string) bool {
	n := strings.ToLower(name)
	for _, ext := range archiveExts {
		if strings.HasSuffix(n, ext) {
			return true
		}
	}
	return false
}

// --- scanning ---

type gitInfo struct {
	lastCommit time.Time
	branch     string
	dirty      int
	unpushed   int
	hasRemote  bool
	stashes    int
	upstream   string
	ahead      int
	behind     int
	branches   int
	worktrees  int
	webURL     string
	hy         hygiene
}

type scanResult struct {
	size     int64
	lastFile time.Time
	stack    []string
	commands []Command
	git      *gitInfo
}

var sem = make(chan struct{}, 8)

// scan reads one item. withSize also walks noisy dirs (node_modules, build, …)
// to total the size; the board skips that because it is slow and unused there.
func scan(it *Item, withSize bool) scanResult {
	sem <- struct{}{}
	defer func() { <-sem }()
	var r scanResult
	dirStats(it.Path, false, withSize, &r.size, &r.lastFile)
	if it.Kind == KindGit || it.Kind == KindProj {
		r.stack = detectStack(it.Path)
		r.commands = detectCommands(it.Path)
	}
	if it.Kind == KindGit {
		g := readGit(it.Path)
		r.git = &g
	}
	return r
}

func scanAll(items []*Item, withSize bool) {
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
			n := noisy || noisyDirs[e.Name()]
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

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	// Read-only: never take the index lock, never prompt for credentials.
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

func readGit(dir string) gitInfo {
	var g gitInfo
	if s, err := git(dir, "log", "-1", "--format=%ct"); err == nil && s != "" {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			g.lastCommit = time.Unix(n, 0)
		}
	}
	g.branch, _ = git(dir, "rev-parse", "--abbrev-ref", "HEAD")
	status, statusErr := git(dir, "status", "--porcelain")
	if statusErr == nil {
		g.dirty = countLines(status)
	}
	remotes, _ := git(dir, "remote")
	g.hasRemote = remotes != ""
	// Commits reachable from any local branch but from no remote branch.
	if s, err := git(dir, "rev-list", "--count", "--branches", "--not", "--remotes"); err == nil {
		g.unpushed, _ = strconv.Atoi(s)
	}
	if s, err := git(dir, "stash", "list"); err == nil {
		g.stashes = countLines(s)
	}
	if s, err := git(dir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"); err == nil {
		g.upstream = s
		if s, err := git(dir, "rev-list", "--left-right", "--count", "HEAD...@{u}"); err == nil {
			g.ahead, g.behind = parseCounts(s)
		}
	}
	if s, err := git(dir, "for-each-ref", "--format=%(refname:short)", "refs/heads"); err == nil {
		g.branches = countLines(s)
	}
	if s, err := git(dir, "worktree", "list", "--porcelain"); err == nil {
		g.worktrees = max(0, strings.Count("\n"+s, "\nworktree ")-1)
	}
	if g.hasRemote {
		url, err := git(dir, "remote", "get-url", "origin")
		if err != nil {
			url, _ = git(dir, "remote", "get-url", strings.Fields(remotes)[0])
		}
		g.webURL = webURL(url)
	}
	g.hy = readHygiene(dir, status, &g)
	return g
}

// parseCounts reads `rev-list --left-right --count` output: "<left>\t<right>".
func parseCounts(s string) (left, right int) {
	f := strings.Fields(s)
	if len(f) == 2 {
		left, _ = strconv.Atoi(f[0])
		right, _ = strconv.Atoi(f[1])
	}
	return left, right
}

// webURL turns a git remote into a browsable https URL:
// git@github.com:User/repo.git → https://github.com/User/repo.
func webURL(remote string) string {
	u := strings.TrimSuffix(strings.TrimSpace(remote), ".git")
	switch {
	case strings.HasPrefix(u, "https://"), strings.HasPrefix(u, "http://"):
		p, err := url.Parse(u)
		if err != nil {
			return ""
		}
		p.User = nil // never show tokens embedded in the remote
		return p.String()
	case strings.HasPrefix(u, "ssh://"):
		u = strings.TrimPrefix(u, "ssh://")
		if at := strings.Index(u, "@"); at >= 0 {
			u = u[at+1:]
		}
		host, path, _ := strings.Cut(u, "/")
		host, _, _ = strings.Cut(host, ":") // drop port
		return "https://" + host + "/" + path
	case strings.Contains(u, ":"):
		if at := strings.Index(u, "@"); at >= 0 {
			u = u[at+1:]
		}
		host, path, _ := strings.Cut(u, ":")
		return "https://" + host + "/" + path
	}
	return ""
}

// detectStack names the toolchains a project uses, from marker files in its
// root and its direct subfolders (monorepos like app/ + web/).
func detectStack(dir string) []string {
	out := stackAt(dir)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || noisyDirs[e.Name()] {
			continue
		}
		for _, s := range stackAt(filepath.Join(dir, e.Name())) {
			if !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
	}
	return out
}

func stackAt(dir string) []string {
	has := func(names ...string) bool {
		for _, n := range names {
			if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
				return true
			}
		}
		return false
	}
	var out []string
	add := func(ok bool, name string) {
		if ok {
			out = append(out, name)
		}
	}
	add(has("pubspec.yaml"), "flutter")
	if b, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		pkg := string(b)
		switch {
		case strings.Contains(pkg, `"vscode"`) && strings.Contains(pkg, `"engines"`):
			out = append(out, "vscode-ext")
		case strings.Contains(pkg, `"@sveltejs/kit"`):
			out = append(out, "sveltekit")
		case strings.Contains(pkg, `"svelte"`):
			out = append(out, "svelte")
		case strings.Contains(pkg, `"next"`):
			out = append(out, "next")
		case strings.Contains(pkg, `"react"`):
			out = append(out, "react")
		default:
			out = append(out, "node")
		}
	}
	add(has("go.mod"), "go")
	add(has("Cargo.toml"), "rust")
	add(has("pyproject.toml", "requirements.txt", "manage.py"), "python")
	add(has("platformio.ini"), "platformio")
	add(has("docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"), "docker")
	add(has("firebase.json"), "firebase")
	add(has("wrangler.toml", "wrangler.json", "wrangler.jsonc"), "cloudflare")
	return out
}

func (it *Item) apply(r scanResult) {
	it.SizeBytes, it.LastFile, it.Stack, it.Commands = r.size, r.lastFile, r.stack, r.commands
	if g := r.git; g != nil {
		it.LastCommit, it.Branch, it.Dirty = g.lastCommit, g.branch, g.dirty
		it.Unpushed, it.HasRemote, it.Stashes = g.unpushed, g.hasRemote, g.stashes
		it.Upstream, it.Ahead, it.Behind = g.upstream, g.ahead, g.behind
		it.Branches, it.Worktrees, it.WebURL = g.branches, g.worktrees, g.webURL
	}
	it.Scanned = true
	it.finalize()
	if r.git != nil {
		it.Warnings = it.warnings(r.git.hy, time.Now())
	}
}

// finalize derives Activity and Risks from the raw scan fields.
func (it *Item) finalize() {
	it.Activity = it.LastFile
	if it.Kind == KindGit && !it.LastCommit.IsZero() {
		it.Activity = it.LastCommit
		if it.Dirty > 0 && it.LastFile.After(it.Activity) {
			it.Activity = it.LastFile
		}
	}
	it.Risks = nil
	switch it.Kind {
	case KindGit:
		if it.Dirty > 0 {
			it.Risks = append(it.Risks, fmt.Sprintf("%d uncommitted", it.Dirty))
		}
		if !it.HasRemote {
			it.Risks = append(it.Risks, "no remote")
		} else if it.Unpushed > 0 {
			it.Risks = append(it.Risks, fmt.Sprintf("%d unpushed", it.Unpushed))
		}
		if it.Stashes > 0 {
			it.Risks = append(it.Risks, fmt.Sprintf("%d stashed", it.Stashes))
		}
	case KindProj, KindFolder:
		it.Risks = append(it.Risks, "not in git")
	}
}

// --- duplicate detection ---

var versionSuffix = regexp.MustCompile(`(?i)[-_ .]?(old|backup|bak|copy|kopie|new|final|concept|v\d+|\d+)$`)
var separators = strings.NewReplacer("-", "", "_", "", " ", "", ".", "")

// dupeKey normalises "_transpafleet", "transpafleet_v1.zip" and
// "transpafleet-old" to the same key.
func dupeKey(rel string) string {
	n := strings.ToLower(filepath.Base(rel))
	for _, ext := range archiveExts {
		n = strings.TrimSuffix(n, ext)
	}
	n = strings.TrimLeft(n, "_-.")
	for {
		m := versionSuffix.ReplaceAllString(n, "")
		if m == n || m == "" {
			break
		}
		n = m
	}
	return separators.Replace(n)
}

func markDupes(items []*Item) {
	groups := map[string][]*Item{}
	for _, it := range items {
		it.Dupes = nil
		if !it.Gone {
			k := dupeKey(it.Rel)
			groups[k] = append(groups[k], it)
		}
	}
	for _, g := range groups {
		if len(g) < 2 {
			continue
		}
		for _, it := range g {
			for _, other := range g {
				if other != it {
					it.Dupes = append(it.Dupes, other.Rel)
				}
			}
		}
	}
}
