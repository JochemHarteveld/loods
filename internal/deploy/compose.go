package deploy

import (
	"fmt"
	"os/exec"
	"strings"
)

// ComposeUpstream asks compose which address on this machine reaches a
// service's port. Compose publishes on a port it picked (or one the file
// fixed), and only it knows which — reading the file ourselves would be a
// second, worse implementation of something compose already answers.
func ComposeUpstream(dir, service string, port int) (string, error) {
	cmd := exec.Command("docker", "compose", "port", service, fmt.Sprint(port))
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("docker compose port %s %d: %w", service, port, err)
	}
	addr := strings.TrimSpace(string(out))
	if addr == "" {
		return "", fmt.Errorf("%s does not publish port %d", service, port)
	}
	// Compose answers 0.0.0.0:32769 for a port published on every interface;
	// the edge only ever talks to this machine.
	if host, rest, ok := strings.Cut(addr, ":"); ok && (host == "0.0.0.0" || host == "::") {
		addr = "127.0.0.1:" + rest
	}
	return addr, nil
}
