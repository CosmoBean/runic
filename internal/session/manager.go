package session

import (
	"fmt"
	"sync"

	"github.com/cosmobean/runic/internal/protocol"
)

// Manager tracks all active PTY sessions.
type Manager struct {
	sessions map[string]*Session
	mu       sync.RWMutex

	defaultShell string
	maxSessions  int
	bufSize      int
	chanSize     int
	throttleMB   int
	nextID       int
}

type ManagerOpts struct {
	DefaultShell string
	MaxSessions  int
	BufSize      int
	ChanSize     int
	ThrottleMB   int
}

func NewManager(opts ManagerOpts) *Manager {
	return &Manager{
		sessions:     make(map[string]*Session),
		defaultShell: opts.DefaultShell,
		maxSessions:  opts.MaxSessions,
		bufSize:      opts.BufSize,
		chanSize:     opts.ChanSize,
		throttleMB:   opts.ThrottleMB,
	}
}

func (m *Manager) Create(req protocol.CreateRequest) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.sessions) >= m.maxSessions {
		return nil, fmt.Errorf("max sessions (%d) reached", m.maxSessions)
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

	sess, err := NewSession(SessionOpts{
		ID:         id,
		Name:       name,
		Shell:      shell,
		Cols:       cols,
		Rows:       rows,
		BufSize:    m.bufSize,
		ChanSize:   m.chanSize,
		ThrottleMB: m.throttleMB,
	})
	if err != nil {
		return nil, err
	}

	m.sessions[id] = sess

	// Auto-cleanup when session exits
	go func() {
		<-sess.Done()
		m.mu.Lock()
		delete(m.sessions, id)
		m.mu.Unlock()
	}()

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
		list = append(list, protocol.SessionInfo{
			ID:      s.ID,
			Name:    s.Name,
			Type:    "pty",
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

func (m *Manager) KillAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, s := range m.sessions {
		_ = s.Kill()
		delete(m.sessions, id)
	}
}
