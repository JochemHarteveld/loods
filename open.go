package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
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
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
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
