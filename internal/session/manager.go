package session

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/cosmobean/runic/internal/protocol"
)

const (
	BackendAuto = "auto"
	BackendPTY  = "pty"
	BackendTmux = "tmux"
)

// Manager tracks all active terminal sessions.
type Manager struct {
	sessions map[string]*Session
	mu       sync.RWMutex

	defaultShell string
	defaultMode  string
	maxSessions  int
	bufSize      int
	chanSize     int
	throttleMB   int
	loginShell   bool
	startDir     string
	nextID       int
	tmuxPath     string
}

type ManagerOpts struct {
	DefaultShell string
	SessionMode  string
	MaxSessions  int
	BufSize      int
	ChanSize     int
	ThrottleMB   int
	LoginShell   bool
	StartDir     string
}

func NewManager(opts ManagerOpts) *Manager {
	tmuxPath := findTmuxBinary("")
	mode := normalizeBackend(opts.SessionMode)
	if mode == "" {
		mode = BackendAuto
	}
	startDir := strings.TrimSpace(opts.StartDir)
	if startDir == "" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			startDir = home
		} else {
			startDir = "/"
		}
	}

	m := &Manager{
		sessions:     make(map[string]*Session),
		defaultShell: opts.DefaultShell,
		defaultMode:  mode,
		maxSessions:  opts.MaxSessions,
		bufSize:      opts.BufSize,
		chanSize:     opts.ChanSize,
		throttleMB:   opts.ThrottleMB,
		loginShell:   opts.LoginShell,
		startDir:     startDir,
		tmuxPath:     tmuxPath,
	}
	m.discoverExistingTmux()
	return m
}

func (m *Manager) Create(req protocol.CreateRequest) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.sessions) >= m.maxSessions {
		return nil, fmt.Errorf("max sessions (%d) reached", m.maxSessions)
	}

	backend, err := m.resolveBackend(req.Type)
	if err != nil {
		return nil, err
	}

	m.nextID++
	id := fmt.Sprintf("s%d", m.nextID)

	shell := req.Shell
	if shell == "" {
		shell = m.defaultShell
	}

	name := req.Name
	if name == "" {
		name = fmt.Sprintf("session-%d", m.nextID)
	}

	cols := uint16(req.Cols)
	rows := uint16(req.Rows)
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}

	sessOpts := SessionOpts{
		ID:         id,
		Name:       name,
		Shell:      shell,
		Cols:       cols,
		Rows:       rows,
		BufSize:    m.bufSize,
		ChanSize:   m.chanSize,
		ThrottleMB: m.throttleMB,
		LoginShell: m.loginShell,
		WorkDir:    m.startDir,
		TmuxBinary: m.tmuxPath,
	}

	var sess *Session
	switch backend {
	case BackendTmux:
		sess, err = NewTmuxSession(sessOpts)
	default:
		sess, err = NewSession(sessOpts)
	}
	if err != nil {
		return nil, err
	}

	m.sessions[id] = sess
	m.trackID(id)
	go m.autoCleanup(id, sess)

	return sess, nil
}

func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	return s, ok
}

func (m *Manager) List() []protocol.SessionInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	list := make([]protocol.SessionInfo, 0, len(m.sessions))
	for _, s := range m.sessions {
		typ := s.SessionType
		if typ == "" {
			typ = BackendPTY
		}
		list = append(list, protocol.SessionInfo{
			ID:      s.ID,
			Name:    s.Name,
			Type:    typ,
			Running: s.IsRunning(),
			Created: s.CreatedAt.Unix(),
			Cols:    int(s.Cols),
			Rows:    int(s.Rows),
		})
	}
	return list
}

func (m *Manager) Kill(id string) error {
	m.mu.Lock()
	s, ok := m.sessions[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("session %s not found", id)
	}
	delete(m.sessions, id)
	m.mu.Unlock()
	return s.Kill()
}

// ShutdownAll stops wrapper sessions while preserving tmux backend sessions.
func (m *Manager) ShutdownAll() {
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for id, s := range m.sessions {
		sessions = append(sessions, s)
		delete(m.sessions, id)
	}
	m.mu.Unlock()

	for _, s := range sessions {
		if s.SessionType == BackendTmux {
			_ = s.ClosePreserve()
			continue
		}
		_ = s.Kill()
	}
}

func (m *Manager) KillAll() {
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for id, s := range m.sessions {
		sessions = append(sessions, s)
		delete(m.sessions, id)
	}
	m.mu.Unlock()

	for _, s := range sessions {
		_ = s.Kill()
	}
}

func (m *Manager) resolveBackend(requested string) (string, error) {
	req := normalizeBackend(requested)
	if req == "" {
		req = m.defaultMode
	}
	if req == BackendAuto {
		if m.tmuxPath != "" {
			return BackendTmux, nil
		}
		return BackendPTY, nil
	}
	if req == BackendTmux {
		if m.tmuxPath == "" {
			return "", fmt.Errorf("tmux backend requested but tmux is not installed")
		}
		return BackendTmux, nil
	}
	if req == BackendPTY {
		return BackendPTY, nil
	}
	return "", fmt.Errorf("unsupported session type: %s", requested)
}

func normalizeBackend(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}

func (m *Manager) discoverExistingTmux() {
	if m.tmuxPath == "" {
		return
	}
	sessions, err := DiscoverTmuxSessions(m.tmuxPath)
	if err != nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, meta := range sessions {
		id := strings.TrimSpace(meta.RunicID)
		if id == "" {
			continue
		}
		if _, exists := m.sessions[id]; exists {
			continue
		}

		sess, err := AttachTmuxSession(SessionOpts{
			ID:         id,
			Name:       meta.RunicName,
			Shell:      m.defaultShell,
			Cols:       80,
			Rows:       24,
			BufSize:    m.bufSize,
			ChanSize:   m.chanSize,
			ThrottleMB: m.throttleMB,
			LoginShell: m.loginShell,
			WorkDir:    m.startDir,
			TmuxBinary: m.tmuxPath,
		}, meta.SessionName, meta.CreatedAt)
		if err != nil {
			continue
		}

		m.sessions[id] = sess
		m.trackID(id)
		go m.autoCleanup(id, sess)
	}
}

func (m *Manager) autoCleanup(id string, sess *Session) {
	<-sess.Done()
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
}

func (m *Manager) trackID(id string) {
	if !strings.HasPrefix(id, "s") {
		return
	}
	n, err := strconv.Atoi(strings.TrimPrefix(id, "s"))
	if err != nil {
		return
	}
	if n > m.nextID {
		m.nextID = n
	}
}

func findTmuxBinary(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if p, err := exec.LookPath("tmux"); err == nil {
		return p
	}
	for _, dir := range []string{
		"/opt/homebrew/bin",
		"/usr/local/bin",
		"/home/linuxbrew/.linuxbrew/bin",
		"/usr/bin",
		"/bin",
	} {
		candidate := filepath.Join(dir, "tmux")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0111 != 0 {
			return candidate
		}
	}
	return ""
}
