package run

import (
	"fmt"
	"path"
	"strings"
)

// Ownership: which files a job may change.
//
// `owns:` is a list of path globs, relative to the project root, that a job is
// the only one allowed to touch. It exists because the expensive mistake in a
// parallel run is not a merge conflict — git shouts about those — it is two
// lanes quietly deciding the same question two ways, and a merge job that has
// to re-do a quarter of their work to make them agree. An orchestrator can
// write that boundary in a design doc, as prose, and the lanes will still walk
// over it; written here it is checked before a single session starts.
//
// The rule is narrow on purpose: two jobs may own overlapping paths as long as
// one waits for the other, because then they edit those files in turn, which is
// what a normal sequence of jobs does. It is only a mistake when the two can be
// in the water at the same time. A job with no `owns:` claims nothing and is
// never in anybody's way — the field is opt-in, and an old graph keeps working.
//
// The check compares patterns against patterns, never the files on disk: a
// graph is validated when it is submitted, which is before the jobs that are
// going to create half those files have run.

const (
	maxOwnsPerJob = 32
	maxOwnLen     = 200
)

// validateOwns cleans a job's owned paths and refuses the ones that cannot
// mean anything: absolute paths (a job owns part of its project, not part of
// the disk) and paths that climb out of the project.
func (j *Job) validateOwns() error {
	out := make([]string, 0, len(j.Owns))
	seen := map[string]bool{}
	for _, g := range j.Owns {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		if len(g) > maxOwnLen {
			return fmt.Errorf("job %s: owned path %q is longer than %d characters", j.ID, Trim(g, 40), maxOwnLen)
		}
		if strings.HasPrefix(g, "/") {
			return fmt.Errorf("job %s: owned path %q is absolute; owned paths are relative to the project", j.ID, g)
		}
		g = path.Clean(g)
		if g == "." || g == ".." || strings.HasPrefix(g, "../") {
			return fmt.Errorf("job %s: owned path %q is outside the project", j.ID, g)
		}
		if seen[g] {
			continue
		}
		seen[g] = true
		out = append(out, g)
	}
	if len(out) > maxOwnsPerJob {
		return fmt.Errorf("job %s owns %d paths, more than the %d a job may claim; own a folder instead of its files",
			j.ID, len(out), maxOwnsPerJob)
	}
	j.Owns = out
	return nil
}

// ownersDisjoint refuses a graph in which two jobs that may run at the same
// time claim the same files. The error names both jobs and the two patterns
// that meet, because the fix is a decision the orchestrator has to make: give
// the overlap to one of them, or make one wait for the other.
func (r *Run) ownersDisjoint() error {
	for i := range r.Jobs {
		for k := i + 1; k < len(r.Jobs); k++ {
			a, b := r.Jobs[i], r.Jobs[k]
			if len(a.Owns) == 0 || len(b.Owns) == 0 {
				continue
			}
			if r.dependsOn(a.ID, b.ID) || r.dependsOn(b.ID, a.ID) {
				continue // they take turns; sharing files is what a sequence is for
			}
			for _, ga := range a.Owns {
				for _, gb := range b.Owns {
					if !globsOverlap(ga, gb) {
						continue
					}
					if ga == gb {
						return fmt.Errorf("jobs %s and %s can run at the same time and both own %q: "+
							"give it to one of them, or make one need the other", a.ID, b.ID, ga)
					}
					return fmt.Errorf("jobs %s and %s can run at the same time and %s owns %q while %s owns %q, "+
						"which are the same files: give the overlap to one of them, or make one need the other",
						a.ID, b.ID, a.ID, ga, b.ID, gb)
				}
			}
		}
	}
	return nil
}

// dependsOn reports whether job a waits for job b, directly or through other
// jobs. The graph is known to be small and is checked for cycles separately,
// so a plain walk with a seen-set is enough.
func (r Run) dependsOn(a, b string) bool {
	seen := map[string]bool{}
	var walk func(id string) bool
	walk = func(id string) bool {
		if seen[id] {
			return false
		}
		seen[id] = true
		i, ok := r.JobIndex(id)
		if !ok {
			return false
		}
		for _, need := range r.Jobs[i].Needs {
			if need == b || walk(need) {
				return true
			}
		}
		return false
	}
	return walk(a)
}

// globsOverlap reports whether two patterns can name the same file. It is
// pessimistic where it cannot tell: a refused graph costs the orchestrator one
// edit, an unnoticed shared file costs a merge job an hour.
//
// A pattern that names a folder — a last segment with no wildcard and no dot
// in it, like "src/lib/ui" or "e2e" — owns everything under it, as if it were
// written "src/lib/ui/**". A last segment with a dot is a file, and nothing
// lives under a file, so "src/**/*.css" and "src/lib/ui/Hud.svelte" are
// separate lanes. "**" stands for any number of segments wherever it is
// written, so a pattern can always say explicitly what it means.
func globsOverlap(a, b string) bool {
	return segmentsOverlap(segmentsOf(a), segmentsOf(b))
}

// segmentsOf splits a pattern and gives a folder pattern its implied subtree.
func segmentsOf(g string) []string {
	segs := strings.Split(g, "/")
	if last := segs[len(segs)-1]; folderish(last) {
		segs = append(segs, "**")
	}
	return segs
}

// folderish reports whether a segment reads as a folder name rather than a
// file name: no wildcard, and no dot other than a leading one.
func folderish(s string) bool {
	return s != "" && !hasMeta(s) && !strings.Contains(strings.TrimPrefix(s, "."), ".")
}

func segmentsOverlap(x, y []string) bool {
	switch {
	case len(x) == 0 && len(y) == 0:
		return true
	case len(x) == 0:
		return allDeep(y)
	case len(y) == 0:
		return allDeep(x)
	case x[0] == "**":
		return segmentsOverlap(x[1:], y) || segmentsOverlap(x, y[1:])
	case y[0] == "**":
		return segmentsOverlap(x, y[1:]) || segmentsOverlap(x[1:], y)
	default:
		return segmentOverlap(x[0], y[0]) && segmentsOverlap(x[1:], y[1:])
	}
}

// allDeep reports whether what is left of a pattern is nothing but "**", which
// is the only way a pattern that has run out of path can still match.
func allDeep(segs []string) bool {
	for _, s := range segs {
		if s != "**" {
			return false
		}
	}
	return true
}

// segmentOverlap compares one path segment of each pattern.
func segmentOverlap(p, q string) bool {
	if p == q {
		return true
	}
	metaP, metaQ := hasMeta(p), hasMeta(q)
	switch {
	case !metaP && !metaQ:
		return false
	case metaP && !metaQ:
		ok, err := path.Match(p, q)
		return ok || err != nil
	case !metaP && metaQ:
		ok, err := path.Match(q, p)
		return ok || err != nil
	}
	// Two patterns: there is no witness to test, so compare the literal ends
	// they both insist on. "*.ts" and "*.svelte" cannot meet; "Hud*" and
	// "*.svelte" can.
	preP, sufP := literalEnds(p)
	preQ, sufQ := literalEnds(q)
	prefixOK := strings.HasPrefix(preP, preQ) || strings.HasPrefix(preQ, preP)
	suffixOK := strings.HasSuffix(sufP, sufQ) || strings.HasSuffix(sufQ, sufP)
	return prefixOK && suffixOK
}

func hasMeta(s string) bool { return strings.ContainsAny(s, "*?[") }

// literalEnds is the fixed text a pattern starts and ends with: everything
// before its first wildcard and everything after its last one.
func literalEnds(s string) (prefix, suffix string) {
	first := strings.IndexAny(s, "*?[")
	last := strings.LastIndexAny(s, "*?[")
	return s[:first], s[last+1:]
}

// Owners lists, per job id, the paths that job claims. Only the jobs that claim
// something are in it, so an empty map means nobody wrote a boundary.
func (r Run) Owners() map[string][]string {
	out := map[string][]string{}
	for _, j := range r.Jobs {
		if len(j.Owns) > 0 {
			out[j.ID] = j.Owns
		}
	}
	return out
}
