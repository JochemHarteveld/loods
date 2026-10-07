package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

func openTarget(it *Item, target string) error {
	switch target {
	case "code":
		return launch("", "code", it.Path)
	case "terminal":
		if _, err := exec.LookPath("gnome-terminal"); err == nil {
			return launch("", "gnome-terminal", "--working-directory="+it.Path)
		}
		return launch(it.Path, "x-terminal-emulator")
	case "folder":
		return launch("", "xdg-open", it.Path)
	case "github":
		if it.WebURL == "" {
			return errors.New("no remote")
		}
		return launch("", "xdg-open", it.WebURL)
	}
	return fmt.Errorf("unknown target %q", target)
}

// launch starts a program detached from loods, so it outlives the server
// and Ctrl+C in the loods terminal does not reach it.
func launch(dir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = cleanEnv(os.Environ())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// cleanEnv undoes what the VS Code snap leaks into its terminals: GTK/GIO paths
// into /snap make native programs (gnome-terminal) load snap libs and crash.
// Matters when loods itself was started from a VS Code terminal.
func cleanEnv(env []string) []string {
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

// openWindow shows the board as a chromeless app window, falling back to the
// default browser.
func openWindow(url string) error {
	for _, b := range []string{"google-chrome", "chromium", "chromium-browser", "brave-browser", "microsoft-edge"} {
		if _, err := exec.LookPath(b); err == nil {
			return launch("", b, "--app="+url, "--window-size=1440,900")
		}
	}
	fmt.Fprintln(os.Stderr, "no Chromium-based browser found; opening default browser")
	return launch("", "xdg-open", url)
}
