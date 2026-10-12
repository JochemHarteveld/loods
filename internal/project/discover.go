package project

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var projectMarkers = []string{
	"package.json", "pubspec.yaml", "go.mod", "Cargo.toml", "pyproject.toml",
	"requirements.txt", "composer.json", "pom.xml", "build.gradle", "Makefile",
	"docker-compose.yml", "compose.yml", "CMakeLists.txt", "platformio.ini",
}

var archiveExts = []string{".tar.gz", ".tgz", ".zip", ".tar", ".7z", ".rar"}

// Discover finds candidate items under root. Folders that only group other
// projects (like ~/Projects/transpaclean) are descended into, not listed.
func Discover(root string, maxDepth int, skip string) []*Item {
	var out []*Item
	walk(root, root, 0, maxDepth, skip, &out)
	sort.Slice(out, func(a, b int) bool { return out[a].Rel < out[b].Rel })
	return out
}

// Board is Discover narrowed to what the board works with: projects, not plain
// folders or archive files.
func Board(root string, maxDepth int, skip string) []*Item {
	var out []*Item
	for _, it := range Discover(root, maxDepth, skip) {
		if it.IsProject() {
			out = append(out, it)
		}
	}
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
