// Package textfmt formats the small things the CLI and the board both say:
// how long ago something happened, how long it took, one or many.
package textfmt

import (
	"fmt"
	"strings"
	"time"
)

// Ago is a compact "just now" / "4m" / "2h" / "3d".
func Ago(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// Span formats a longer stretch like the UI does: 5d, 3w, 2mo.
func Span(d time.Duration) string {
	days := int(d.Hours() / 24)
	switch {
	case days < 14:
		return fmt.Sprintf("%dd", days)
	case days < 60:
		return fmt.Sprintf("%dw", days/7)
	default:
		return fmt.Sprintf("%dmo", days/30)
	}
}

// Short trims an id to something readable in a sentence.
func Short(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func Plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// Trim folds a text onto one line of at most n characters.
func Trim(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// OrDash is for table cells: an em dash where there is nothing to show.
func OrDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
