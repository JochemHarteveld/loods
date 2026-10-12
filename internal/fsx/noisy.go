// Package fsx holds the filesystem facts several packages need to agree on.
package fsx

// noisy dirs hold generated or vendored files: they count toward a project's
// size, but a change in them is not someone working on the project, and
// nothing in them is worth scanning for commands, stacks or secrets.
var noisy = map[string]bool{
	".git": true, "node_modules": true, "build": true, ".dart_tool": true,
	"target": true, ".venv": true, "venv": true, "dist": true, ".svelte-kit": true,
	".next": true, "__pycache__": true, ".gradle": true, "Pods": true,
	"vendor": true, ".idea": true, ".pio": true,
}

// Noisy reports whether a directory of this name is generated or vendored.
func Noisy(name string) bool { return noisy[name] }
