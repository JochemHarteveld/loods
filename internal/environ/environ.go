// Package environ fixes up the environment loods hands to the programs it starts.
package environ

import "strings"

// Clean undoes what the VS Code snap leaks into its terminals: GTK/GIO paths
// into /snap make native programs (gnome-terminal) load snap libs and crash.
// Matters when loods itself was started from a VS Code terminal.
func Clean(env []string) []string {
	drop := map[string]bool{
		"GTK_PATH": true, "GTK_EXE_PREFIX": true, "GTK_IM_MODULE_FILE": true, "GIO_MODULE_DIR": true,
		"GDK_PIXBUF_MODULE_FILE": true, "GDK_PIXBUF_MODULEDIR": true, "GSETTINGS_SCHEMA_DIR": true, "LOCPATH": true,
	}
	restore := map[string]string{} // XDG_DATA_DIRS_VSCODE_SNAP_ORIG holds the value before the snap changed it
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if base, ok := strings.CutSuffix(k, "_VSCODE_SNAP_ORIG"); ok {
			restore[base] = v
		}
	}
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if _, replaced := restore[k]; drop[k] || replaced || strings.HasSuffix(k, "_VSCODE_SNAP_ORIG") {
			continue
		}
		out = append(out, kv)
	}
	for k, v := range restore {
		if v != "" {
			out = append(out, k+"="+v)
		}
	}
	return out
}
