//go:build !windows

package session

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"al.essio.dev/pkg/shellescape"
	"github.com/creack/pty"
)

// Session represents a single PTY terminal session.
type Session struct {
	ID          string
	Name        string
	SessionType string
	CreatedAt   time.Time
	Cols        uint16
	Rows        uint16

	cmd      *exec.Cmd
	ptmx     *os.File
	output   chan []byte   // buffered channel for read -> broadcast goroutine
	done     chan struct{} // closed when PTY process exits
	mu       sync.Mutex
	closed   bool
	running  bool
	tmuxBin  string
	tmuxName string
	subs     map[uint64]chan []byte
	nextSub  atomic.Uint64

	// Stats
	BytesRead    int64
	BytesWritten int64

	bufSize    int
	chanSize   int
	throttleMB int
}

type SessionOpts struct {
	ID         string
	Name       string
	Shell      string
	LoginShell bool
	WorkDir    string
	Cols       uint16
	Rows       uint16
	BufSize    int // PTY read buffer (default 16384)
	ChanSize   int // Output channel slots (default 64)
	ThrottleMB int // Throttle threshold in MB (default 1)
	TmuxBinary string
}

type TmuxSessionMeta struct {
	SessionName string
	RunicID     string
	RunicName   string
	CreatedAt   time.Time
}

// NewSession creates and starts a new PTY shell session.
func NewSession(opts SessionOpts) (*Session, error) {
	opts = normalizeOpts(opts)

	shellBin, shellArgs, err := resolveShellCommand(opts.Shell, opts.LoginShell)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(shellBin, shellArgs...)
	cmd.Env = prepareSessionEnv()
	cmd.Dir = opts.WorkDir

	ptmx, err := startPTY(cmd, opts.Cols, opts.Rows)
	if err != nil {
		return nil, err
	}

	s := newBaseSession(opts, cmd, ptmx)
	s.SessionType = BackendPTY
	s.startLoops()
	return s, nil
}

// NewTmuxSession creates a new tmux-backed session.
func NewTmuxSession(opts SessionOpts) (*Session, error) {
	opts = normalizeOpts(opts)
	if opts.TmuxBinary = findTmuxBinary(opts.TmuxBinary); opts.TmuxBinary == "" {
		return nil, fmt.Errorf("tmux not found in PATH")
	}

	tmuxName := tmuxSessionName(opts.ID)
	args := []string{"new-session", "-d", "-s", tmuxName, "-x", strconv.Itoa(int(opts.Cols)), "-y", strconv.Itoa(int(opts.Rows))}
	if opts.WorkDir != "" {
		args = append(args, "-c", opts.WorkDir)
	}
	if opts.Shell != "" {
		shellBin, shellArgs, err := resolveShellCommand(opts.Shell, opts.LoginShell)
		if err != nil {
			return nil, err
		}
		args = append(args, shellescape.QuoteCommand(append([]string{shellBin}, shellArgs...)))
	}
	createCmd := exec.Command(opts.TmuxBinary, args...)
	createCmd.Env = prepareSessionEnv()
	createCmd.Dir = opts.WorkDir
	if out, err := createCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("creating tmux session: %w: %s", err, strings.TrimSpace(string(out)))
	}

	_ = setTmuxMetadata(opts.TmuxBinary, tmuxName, opts.ID, opts.Name)

	s, err := AttachTmuxSession(opts, tmuxName, time.Now())
	if err != nil {
		_ = exec.Command(opts.TmuxBinary, "kill-session", "-t", tmuxName).Run()
		return nil, err
	}
	return s, nil
}

// AttachTmuxSession attaches a Runic wrapper to an existing tmux session.
func AttachTmuxSession(opts SessionOpts, tmuxName string, createdAt time.Time) (*Session, error) {
	opts = normalizeOpts(opts)
	if opts.TmuxBinary = findTmuxBinary(opts.TmuxBinary); opts.TmuxBinary == "" {
		return nil, fmt.Errorf("tmux not found in PATH")
	}
	if opts.Name == "" {
		opts.Name = tmuxName
	}

	attachCmd := exec.Command(opts.TmuxBinary, "attach-session", "-t", tmuxName)
	attachCmd.Env = prepareSessionEnv()
	attachCmd.Dir = opts.WorkDir
	ptmx, err := startPTY(attachCmd, opts.Cols, opts.Rows)
	if err != nil {
		return nil, err
	}

	s := newBaseSession(opts, attachCmd, ptmx)
	s.SessionType = BackendTmux
	s.tmuxBin = opts.TmuxBinary
	s.tmuxName = tmuxName
	if !createdAt.IsZero() {
		s.CreatedAt = createdAt
	}
	s.startLoops()
	return s, nil
}

// DiscoverTmuxSessions lists existing runic tmux sessions.
func DiscoverTmuxSessions(tmuxBinary string) ([]TmuxSessionMeta, error) {
	if tmuxBinary == "" {
		bin := findTmuxBinary("")
		if bin == "" {
			return nil, nil
		}
		tmuxBinary = bin
	}

	cmd := exec.Command(tmuxBinary, "list-sessions", "-F", "#{session_name}\t#{@runic_id}\t#{@runic_name}\t#{session_created}")
	out, err := cmd.CombinedOutput()
	if err != nil {
		// tmux exits non-zero when no sessions exist; treat as empty.
		if bytes.Contains(bytes.ToLower(out), []byte("no server running")) || len(bytes.TrimSpace(out)) == 0 {
			return nil, nil
		}
		return nil, fmt.Errorf("list tmux sessions: %w: %s", err, strings.TrimSpace(string(out)))
	}

	list := make([]TmuxSessionMeta, 0)
	s := bufio.NewScanner(bytes.NewReader(out))
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 4)
		if len(parts) != 4 {
			continue
		}
		sName := strings.TrimSpace(parts[0])
		rID := sanitizeTmuxOpt(parts[1])
		rName := sanitizeTmuxOpt(parts[2])
		if rID == "" {
			if strings.HasPrefix(sName, "runic-") {
				rID = strings.TrimPrefix(sName, "runic-")
			} else {
				continue
			}
		}
		if rName == "" {
			rName = sName
		}

		created := time.Time{}
		if ts, err := strconv.ParseInt(strings.TrimSpace(parts[3]), 10, 64); err == nil && ts > 0 {
			created = time.Unix(ts, 0)
		}

		list = append(list, TmuxSessionMeta{
			SessionName: sName,
			RunicID:     rID,
			RunicName:   rName,
			CreatedAt:   created,
		})
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	return list, nil
}

func sanitizeTmuxOpt(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || v == "(null)" {
		return ""
	}
	return v
}

func setTmuxMetadata(tmuxBin, tmuxName, runicID, runicName string) error {
	if runicID != "" {
		if out, err := exec.Command(tmuxBin, "set-option", "-t", tmuxName, "@runic_id", runicID).CombinedOutput(); err != nil {
			return fmt.Errorf("set tmux runic_id: %w: %s", err, strings.TrimSpace(string(out)))
		}
	}
	if runicName != "" {
		if out, err := exec.Command(tmuxBin, "set-option", "-t", tmuxName, "@runic_name", runicName).CombinedOutput(); err != nil {
			return fmt.Errorf("set tmux runic_name: %w: %s", err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

func normalizeOpts(opts SessionOpts) SessionOpts {
	if opts.Cols == 0 {
		opts.Cols = 80
	}
	if opts.Rows == 0 {
		opts.Rows = 24
	}
	if opts.BufSize == 0 {
		opts.BufSize = 16384
	}
	if opts.ChanSize == 0 {
		opts.ChanSize = 64
	}
	if opts.ThrottleMB == 0 {
		opts.ThrottleMB = 1
	}
	if opts.Shell == "" {
		opts.Shell = os.Getenv("SHELL")
		if opts.Shell == "" {
			opts.Shell = "/bin/sh"
		}
	}
	if opts.WorkDir == "" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			opts.WorkDir = home
		} else {
			opts.WorkDir = "/"
		}
	}
	return opts
}

func startPTY(cmd *exec.Cmd, cols, rows uint16) (*os.File, error) {
	winSize := &pty.Winsize{Cols: cols, Rows: rows}
	ptmx, err := pty.StartWithSize(cmd, winSize)
	if err != nil {
		return nil, fmt.Errorf("starting pty: %w", err)
	}
	return ptmx, nil
}

func newBaseSession(opts SessionOpts, cmd *exec.Cmd, ptmx *os.File) *Session {
	return &Session{
		ID:         opts.ID,
		Name:       opts.Name,
		CreatedAt:  time.Now(),
		Cols:       opts.Cols,
		Rows:       opts.Rows,
		cmd:        cmd,
		ptmx:       ptmx,
		output:     make(chan []byte, opts.ChanSize),
		done:       make(chan struct{}),
		running:    true,
		subs:       make(map[uint64]chan []byte),
		bufSize:    opts.BufSize,
		chanSize:   opts.ChanSize,
		throttleMB: opts.ThrottleMB,
	}
}

func (s *Session) startLoops() {
	go s.readLoop()
	go s.broadcastLoop()
	go s.waitLoop()
}

func tmuxSessionName(id string) string {
	if id == "" {
		id = "unknown"
	}
	return "runic-" + id
}

// readLoop reads from the PTY and pushes to the output channel.
func (s *Session) readLoop() {
	buf := make([]byte, s.bufSize)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])
			s.BytesRead += int64(n)
			select {
			case s.output <- data:
			default:
			}
		}
		if err != nil {
			if err != io.EOF {
				// Unexpected error, but close cleanly.
			}
			close(s.output)
			return
		}
	}
}

// waitLoop waits for the shell process to exit and cleans up.
func (s *Session) waitLoop() {
	_ = s.cmd.Wait()
	s.mu.Lock()
	s.running = false
	s.mu.Unlock()
	close(s.done)
}

// SubscribeOutput returns a per-subscriber output stream and subscription ID.
func (s *Session) SubscribeOutput() (uint64, <-chan []byte) {
	ch := make(chan []byte, s.chanSize)
	id := s.nextSub.Add(1)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		close(ch)
		return id, ch
	}
	s.subs[id] = ch
	return id, ch
}

// UnsubscribeOutput removes a subscriber and closes its output channel.
func (s *Session) UnsubscribeOutput(id uint64) {
	s.mu.Lock()
	ch, ok := s.subs[id]
	if ok {
		delete(s.subs, id)
	}
	s.mu.Unlock()
	if ok {
		close(ch)
	}
}

func (s *Session) broadcastLoop() {
	for data := range s.output {
		s.mu.Lock()
		for _, ch := range s.subs {
			select {
			case ch <- data:
			default:
				// Slow subscriber; drop this chunk to avoid backpressure.
			}
		}
		s.mu.Unlock()
	}

	s.mu.Lock()
	s.closed = true
	for id, ch := range s.subs {
		delete(s.subs, id)
		close(ch)
	}
	s.mu.Unlock()
}

// Done returns a channel that is closed when the session's process exits.
func (s *Session) Done() <-chan struct{} {
	return s.done
}

// Write sends input to the PTY (keystrokes from the client).
func (s *Session) Write(data []byte) (int, error) {
	n, err := s.ptmx.Write(data)
	s.BytesWritten += int64(n)
	return n, err
}

// Resize changes the PTY window size.
func (s *Session) Resize(cols, rows uint16) error {
	s.mu.Lock()
	s.Cols = cols
	s.Rows = rows
	s.mu.Unlock()

	ws := struct {
		Rows uint16
		Cols uint16
		X    uint16
		Y    uint16
	}{Rows: rows, Cols: cols}

	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		s.ptmx.Fd(),
		syscall.TIOCSWINSZ,
		uintptr(unsafe.Pointer(&ws)),
	)
	if errno != 0 {
		return fmt.Errorf("ioctl TIOCSWINSZ: %v", errno)
	}

	if s.SessionType == BackendTmux && s.tmuxBin != "" && s.tmuxName != "" {
		_ = exec.Command(s.tmuxBin, "resize-window", "-t", s.tmuxName, "-x", strconv.Itoa(int(cols)), "-y", strconv.Itoa(int(rows))).Run()
	}
	return nil
}

// IsRunning returns whether the shell process is still alive.
func (s *Session) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// Kill terminates the session and kills underlying tmux session if applicable.
func (s *Session) Kill() error {
	return s.stop(true)
}

// ClosePreserve detaches wrapper process while preserving tmux backend session.
func (s *Session) ClosePreserve() error {
	return s.stop(false)
}

func (s *Session) stop(killTmux bool) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	cmd := s.cmd
	ptmx := s.ptmx
	sType := s.SessionType
	tmuxBin := s.tmuxBin
	tmuxName := s.tmuxName
	s.mu.Unlock()

	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		go func() {
			select {
			case <-s.done:
			case <-time.After(2 * time.Second):
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
			}
		}()
	}

	if sType == BackendTmux && killTmux && tmuxBin != "" && tmuxName != "" {
		_ = exec.Command(tmuxBin, "kill-session", "-t", tmuxName).Run()
	}

	if ptmx != nil {
		return ptmx.Close()
	}
	return nil
}

func prepareSessionEnv() []string {
	env := os.Environ()
	path := lookupEnv(env, "PATH")
	path = prependPath(path, []string{
		"/opt/homebrew/bin",
		"/usr/local/bin",
		"/home/linuxbrew/.linuxbrew/bin",
	})
	env = upsertEnv(env, "PATH", path)
	env = upsertEnv(env, "TERM", "xterm-256color")
	if home, err := os.UserHomeDir(); err == nil && home != "" && lookupEnv(env, "HOME") == "" {
		env = append(env, "HOME="+home)
	}
	return env
}

func resolveShellCommand(shellSpec string, login bool) (string, []string, error) {
	fields := strings.Fields(strings.TrimSpace(shellSpec))
	if len(fields) == 0 {
		return "", nil, fmt.Errorf("empty shell command")
	}

	name := fields[0]
	args := append([]string{}, fields[1:]...)
	if login && !hasLoginFlag(args) && supportsLoginFlag(name) {
		args = append(args, "-l")
	}
	return name, args, nil
}

func hasLoginFlag(args []string) bool {
	for _, arg := range args {
		if arg == "-l" || arg == "--login" {
			return true
		}
	}
	return false
}

func supportsLoginFlag(shellPath string) bool {
	switch filepath.Base(shellPath) {
	case "bash", "zsh", "sh", "ksh", "dash", "ash", "fish":
		return true
	default:
		return false
	}
}

func lookupEnv(env []string, key string) string {
	prefix := key + "="
	for i := len(env) - 1; i >= 0; i-- {
		if strings.HasPrefix(env[i], prefix) {
			return strings.TrimPrefix(env[i], prefix)
		}
	}
	return ""
}

func upsertEnv(env []string, key, value string) []string {
	prefix := key + "="
	found := false
	for i := range env {
		if strings.HasPrefix(env[i], prefix) {
			env[i] = prefix + value
			found = true
		}
	}
	if !found {
		env = append(env, prefix+value)
	}
	return env
}

func prependPath(pathValue string, prefixes []string) string {
	parts := make([]string, 0, len(prefixes)+8)
	seen := make(map[string]struct{}, len(prefixes)+8)

	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		parts = append(parts, p)
	}

	for _, p := range prefixes {
		add(p)
	}
	for _, p := range strings.Split(pathValue, ":") {
		add(p)
	}
	return strings.Join(parts, ":")
}
