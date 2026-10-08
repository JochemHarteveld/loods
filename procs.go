package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

const maxBacklog = 512 << 10 // terminal output kept per process for late viewers

// Spec is what to run; a Proc is one run of it.
type Spec struct {
	ID      string `json:"id"` // "<project rel>#<command>" or "stack:<name>"
	Project string `json:"project,omitempty"`
	Name    string `json:"name"`
	Run     string `json:"run"`
	Dir     string `json:"dir"`
	Keys    bool   `json:"keys,omitempty"`
	URL     string `json:"url,omitempty"`
}

type Proc struct {
	Spec
	Status    string    `json:"status"` // running | stopping | exited | orphan
	ExitCode  int       `json:"exit_code"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitzero"`
	PID       int       `json:"pid,omitempty"`
	RSS       int64     `json:"rss_bytes,omitempty"`
	Ports     []int     `json:"ports,omitempty"`
	URLs      []string  `json:"urls,omitempty"`
	Warning   string    `json:"warning,omitempty"`
	Stopped   bool      `json:"stopped,omitempty"` // ended because the user stopped it, whatever the exit code

	startTicks uint64 // with PID, identifies the process across loods restarts
	pty        *os.File
	cmd        *exec.Cmd
	out        []byte
	tail       string // end of the previous chunk, so URLs split over two reads are still found
	subs       map[chan []byte]struct{}
	done       chan struct{}
}

func (p *Proc) alive() bool {
	return p.Status == "running" || p.Status == "stopping" || p.Status == "orphan"
}

// Manager owns every process started from the Garage. One lock guards all of it.
type Manager struct {
	mu        sync.Mutex
	saveMu    sync.Mutex
	procs     []*Proc // start order
	stateFile string
	onChange  func()
}

func newManager(stateFile string, onChange func()) *Manager {
	m := &Manager{stateFile: stateFile, onChange: onChange}
	m.adoptOrphans()
	return m
}

func (m *Manager) getLocked(id string) *Proc {
	for _, p := range m.procs {
		if p.ID == id {
			return p
		}
	}
	return nil
}

func (m *Manager) Start(s Spec) error {
	m.mu.Lock()
	if p := m.getLocked(s.ID); p != nil && p.alive() {
		m.mu.Unlock()
		return fmt.Errorf("%s is already running", s.Name)
	}
	m.mu.Unlock()

	// A login shell, so PATH matches a terminal (flutter, pio, nvm, …).
	cmd := exec.Command("bash", "-lc", s.Run)
	cmd.Dir = s.Dir
	cmd.Env = append(cleanEnv(os.Environ()), "TERM=xterm-256color", "COLORTERM=truecolor")
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 120, Rows: 30})
	if err != nil {
		return err
	}
	_, ticks, _ := procStat(cmd.Process.Pid)
	p := &Proc{
		Spec: s, Status: "running", StartedAt: time.Now(), PID: cmd.Process.Pid,
		startTicks: ticks, pty: f, cmd: cmd, subs: map[chan []byte]struct{}{}, done: make(chan struct{}),
	}
	if s.URL != "" {
		p.URLs = []string{s.URL}
	}
	m.mu.Lock()
	if i := slices.IndexFunc(m.procs, func(q *Proc) bool { return q.ID == s.ID }); i >= 0 {
		m.dropSubsLocked(m.procs[i])
		m.procs[i] = p // keep its place in the list
	} else {
		m.procs = append(m.procs, p)
	}
	m.mu.Unlock()
	m.save()
	m.onChange()
	go m.read(p)
	go m.wait(p)
	return nil
}

func (m *Manager) read(p *Proc) {
	buf := make([]byte, 32<<10)
	for {
		n, err := p.pty.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			m.mu.Lock()
			p.out = appendCapped(p.out, chunk, maxBacklog)
			changed := p.inspect(chunk)
			for ch := range p.subs {
				select {
				case ch <- chunk:
				default:
					resync(ch, p.out)
				}
			}
			m.mu.Unlock()
			if changed {
				m.onChange()
			}
		}
		if err != nil {
			return // EIO once the process and its children closed the terminal
		}
	}
}

func (m *Manager) wait(p *Proc) {
	p.cmd.Wait()
	time.Sleep(300 * time.Millisecond) // let read drain the last output
	m.mu.Lock()
	p.Status, p.ExitCode, p.EndedAt = "exited", p.cmd.ProcessState.ExitCode(), time.Now()
	p.RSS, p.Ports = 0, nil
	note := fmt.Sprintf("\r\n\x1b[2m[loods] exited with code %d\x1b[0m\r\n", p.ExitCode)
	if p.Stopped {
		note = "\r\n\x1b[2m[loods] stopped\x1b[0m\r\n"
	}
	p.out = appendCapped(p.out, []byte(note), maxBacklog)
	for ch := range p.subs {
		select {
		case ch <- []byte(note):
		default:
		}
	}
	m.mu.Unlock()
	p.pty.Close()
	// Save before signalling done: StopAll's caller may exit right after.
	m.save()
	close(p.done)
	m.onChange()
}

// Stop asks nicely (Ctrl+C, like a terminal), then SIGTERM, then SIGKILL to the
// whole session. docker compose needs the first step to stop its containers.
func (m *Manager) Stop(id string) error {
	m.mu.Lock()
	p := m.getLocked(id)
	if p == nil || !p.alive() {
		m.mu.Unlock()
		return errors.New("not running")
	}
	if p.Status == "orphan" {
		m.mu.Unlock()
		go m.killOrphan(p)
		return nil
	}
	if p.Status == "stopping" {
		m.mu.Unlock()
		killSession(p.PID, syscall.SIGKILL) // second stop: no more waiting
		return nil
	}
	p.Status, p.Stopped = "stopping", true
	f, pid, done := p.pty, p.PID, p.done
	m.mu.Unlock()
	m.onChange()

	f.Write([]byte{3})
	go func() {
		for _, step := range []struct {
			wait time.Duration
			sig  syscall.Signal
		}{{10 * time.Second, syscall.SIGTERM}, {3 * time.Second, syscall.SIGKILL}} {
			select {
			case <-done:
				return
			case <-time.After(step.wait):
				killSession(pid, step.sig)
			}
		}
	}()
	return nil
}

func (m *Manager) Restart(id string) error {
	m.mu.Lock()
	p := m.getLocked(id)
	if p == nil {
		m.mu.Unlock()
		return errors.New("unknown process")
	}
	spec, wasAlive, done := p.Spec, p.alive(), p.done
	m.mu.Unlock()
	if wasAlive {
		if err := m.Stop(id); err != nil {
			return err
		}
		if done == nil { // orphan: poll until its session is gone
			done = make(chan struct{})
			go func() {
				for len(sessionPids(p.PID)) > 0 {
					time.Sleep(200 * time.Millisecond)
				}
				close(done)
			}()
		}
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			return errors.New("did not stop within 15s")
		}
	}
	return m.Start(spec)
}

func (m *Manager) Remove(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := slices.IndexFunc(m.procs, func(p *Proc) bool { return p.ID == id })
	if i < 0 {
		return errors.New("unknown process")
	}
	if m.procs[i].alive() {
		return errors.New("stop it first")
	}
	m.dropSubsLocked(m.procs[i])
	m.procs = slices.Delete(m.procs, i, i+1)
	go m.onChange()
	return nil
}

// resync catches up a viewer whose queue is full. Dropping it would make the
// browser reconnect and redraw from scratch (a visible flash), so instead its
// queue is replaced in-band by a terminal reset plus the whole backlog.
// Only read() sends on ch and it holds the lock, so after draining there's room.
func resync(ch chan []byte, backlog []byte) {
	for len(ch) > 0 {
		select {
		case <-ch:
		default:
		}
	}
	ch <- append([]byte("\x1bc"), backlog...)
}

func (m *Manager) dropSubsLocked(p *Proc) {
	for ch := range p.subs {
		close(ch)
		delete(p.subs, ch)
	}
}

// Input writes keystrokes to the process terminal.
func (m *Manager) Input(id string, data []byte) error {
	m.mu.Lock()
	p := m.getLocked(id)
	var f *os.File
	if p != nil && (p.Status == "running" || p.Status == "stopping") {
		f = p.pty
	}
	m.mu.Unlock()
	if f == nil {
		return errors.New("not running")
	}
	_, err := f.Write(data)
	return err
}

func (m *Manager) Resize(id string, cols, rows int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p := m.getLocked(id); p != nil && p.Status == "running" && cols > 0 && rows > 0 {
		pty.Setsize(p.pty, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	}
}

// Attach returns the backlog and a channel with everything written after it.
// Both are taken under the lock that read() holds, so nothing is lost or doubled.
func (m *Manager) Attach(id string) (backlog []byte, ch chan []byte, detach func(), ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.getLocked(id)
	if p == nil {
		return nil, nil, nil, false
	}
	ch = make(chan []byte, 256)
	if p.subs == nil {
		p.subs = map[chan []byte]struct{}{}
	}
	p.subs[ch] = struct{}{}
	backlog = append([]byte(nil), p.out...)
	return backlog, ch, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if _, ok := p.subs[ch]; ok {
			delete(p.subs, ch)
			close(ch)
		}
	}, true
}

func (m *Manager) JSON() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := m.procs
	if list == nil {
		list = []*Proc{}
	}
	b, _ := json.Marshal(list)
	return b
}

// Stats refreshes RAM and listening ports of live processes; reports whether anything changed.
func (m *Manager) Stats() bool {
	m.mu.Lock()
	var live []*Proc
	for _, p := range m.procs {
		if p.alive() {
			live = append(live, p)
		}
	}
	m.mu.Unlock()
	if len(live) == 0 {
		return false
	}
	sessions := allSessions()
	changed := false
	var gone []*Proc
	for _, p := range live {
		pids := sessions[p.PID]
		rss, ports := rssBytes(pids), listeningPorts(pids)
		m.mu.Lock()
		if p.RSS>>20 != rss>>20 || !slices.Equal(p.Ports, ports) {
			p.RSS, p.Ports, changed = rss, ports, true
		}
		if p.Status == "orphan" && len(pids) == 0 {
			gone = append(gone, p)
		}
		m.mu.Unlock()
	}
	for _, p := range gone {
		m.markOrphanGone(p)
		changed = true
	}
	return changed
}

// StopAll stops everything (loods is shutting down) and waits up to timeout.
func (m *Manager) StopAll(timeout time.Duration) {
	m.mu.Lock()
	var live []*Proc
	for _, p := range m.procs {
		if p.Status == "running" || p.Status == "stopping" {
			live = append(live, p)
		}
	}
	m.mu.Unlock()
	for _, p := range live {
		m.Stop(p.ID)
	}
	deadline := time.After(timeout)
	for _, p := range live {
		select {
		case <-p.done:
		case <-deadline:
			killSession(p.PID, syscall.SIGKILL)
		}
	}
}

// --- output inspection ---

var (
	ansiRe    = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[@-Z\\-_]`)
	localURL  = regexp.MustCompile(`https?://(?:localhost|127\.0\.0\.1|0\.0\.0\.0|\[::1?\])(?::\d+)?[^\s"'<>\x60]*`)
	portInUse = regexp.MustCompile(`(?i)EADDRINUSE|address already in use|port \d+ is (?:already )?in use`)
)

// inspect picks local URLs and port conflicts out of new output. Caller holds m.mu.
func (p *Proc) inspect(chunk []byte) bool {
	text := p.tail + ansiRe.ReplaceAllString(string(chunk), "")
	changed := false
	for _, u := range localURL.FindAllString(text, -1) {
		u = strings.TrimRight(u, ".,;:)]}'")
		u = strings.NewReplacer("0.0.0.0", "localhost", "[::]", "localhost", "[::1]", "localhost").Replace(u)
		if len(p.URLs) < 5 && !slices.Contains(p.URLs, u) {
			p.URLs = append(p.URLs, u)
			changed = true
		}
	}
	if p.Warning == "" && portInUse.MatchString(text) {
		p.Warning, changed = "port already in use", true
	}
	if len(text) > 256 {
		text = text[len(text)-256:]
	}
	p.tail = text
	return changed
}

func appendCapped(buf, more []byte, limit int) []byte {
	buf = append(buf, more...)
	if len(buf) <= limit {
		return buf
	}
	cut := len(buf) - limit
	if i := slices.Index(buf[cut:], '\n'); i >= 0 && i < 4096 {
		cut += i + 1 // start the backlog at a line boundary
	}
	return append([]byte(nil), buf[cut:]...)
}

// --- sessions ---

// allSessions maps session id → pids, from one pass over /proc.
func allSessions() map[int][]int {
	out := map[int][]int{}
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		var pid int
		if _, err := fmt.Sscan(e.Name(), &pid); err != nil {
			continue
		}
		if sid, _, ok := procStat(pid); ok {
			out[sid] = append(out[sid], pid)
		}
	}
	return out
}

func killSession(sid int, sig syscall.Signal) {
	for _, pid := range sessionPids(sid) {
		syscall.Kill(pid, sig)
	}
}

// --- orphans: processes a previous loods started and never stopped ---

type savedProc struct {
	Spec
	PID        int       `json:"pid"`
	StartTicks uint64    `json:"start_ticks"`
	StartedAt  time.Time `json:"started_at"`
}

// Running reports whether a project has a live process.
func (m *Manager) Running(project string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.procs {
		if p.Project == project && p.alive() {
			return true
		}
	}
	return false
}

func (m *Manager) save() {
	m.saveMu.Lock() // one writer: they share the .tmp file
	defer m.saveMu.Unlock()
	m.mu.Lock()
	var list []savedProc
	for _, p := range m.procs {
		if p.alive() {
			list = append(list, savedProc{Spec: p.Spec, PID: p.PID, StartTicks: p.startTicks, StartedAt: p.StartedAt})
		}
	}
	m.mu.Unlock()
	b, _ := json.MarshalIndent(list, "", "  ")
	if os.MkdirAll(filepath.Dir(m.stateFile), 0o755) != nil {
		return
	}
	tmp := m.stateFile + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, m.stateFile)
	}
}

func (m *Manager) adoptOrphans() {
	b, err := os.ReadFile(m.stateFile)
	if err != nil {
		return
	}
	var list []savedProc
	if json.Unmarshal(b, &list) != nil {
		return
	}
	for _, s := range list {
		// Same pid and same start time: still the process we started, not a reused pid.
		if _, ticks, ok := procStat(s.PID); ok && ticks == s.StartTicks {
			m.procs = append(m.procs, &Proc{
				Spec: s.Spec, Status: "orphan", StartedAt: s.StartedAt, PID: s.PID, startTicks: s.StartTicks,
				out: []byte("\x1b[2m[loods] Started by an earlier loods that did not shut down cleanly.\r\n" +
					"Its output is not available. Stop it (x) or restart it (r).\x1b[0m\r\n"),
			})
		}
	}
}

func (m *Manager) killOrphan(p *Proc) {
	killSession(p.PID, syscall.SIGTERM)
	for i := 0; i < 30 && len(sessionPids(p.PID)) > 0; i++ {
		time.Sleep(100 * time.Millisecond)
	}
	killSession(p.PID, syscall.SIGKILL)
	m.markOrphanGone(p)
}

func (m *Manager) markOrphanGone(p *Proc) {
	m.mu.Lock()
	if p.Status == "orphan" {
		p.Status, p.ExitCode, p.EndedAt, p.RSS, p.Ports = "exited", -1, time.Now(), 0, nil
		p.Stopped = true
	}
	m.mu.Unlock()
	m.save()
	m.onChange()
}
