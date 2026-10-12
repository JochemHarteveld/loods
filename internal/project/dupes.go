package project

import (
	"path/filepath"
	"regexp"
	"strings"
)

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

// MarkDupes points items with the same normalised name at each other, so the
// Graveyard can show "this looks like a copy of that".
func MarkDupes(items []*Item) {
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
