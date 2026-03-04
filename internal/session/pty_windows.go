//go:build windows

package session

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Session represents a single PTY terminal session.
type Session struct {
	ID          string
	Name        string
	SessionType string
	CreatedAt   time.Time
	Cols        uint16
	Rows        uint16

	output  chan []byte
	done    chan struct{}
	mu      sync.Mutex
	closed  bool
	running bool
	subs    map[uint64]chan []byte
	nextSub atomic.Uint64

	BytesRead    int64
	BytesWritten int64
}

type SessionOpts struct {
	ID         string
	Name       string
	Shell      string
	LoginShell bool
	WorkDir    string
	Cols       uint16
	Rows       uint16
	BufSize    int
	ChanSize   int
	ThrottleMB int
	TmuxBinary string
}

type TmuxSessionMeta struct {
	SessionName string
	RunicID     string
	RunicName   string
	CreatedAt   time.Time
}

// NewSession creates and starts a new PTY session.
// Windows support requires a ConPTY backend, which is not wired yet.
func NewSession(opts SessionOpts) (*Session, error) {
	return nil, fmt.Errorf("windows PTY backend not implemented yet (ConPTY required)")
}

// NewTmuxSession is unsupported on Windows.
func NewTmuxSession(opts SessionOpts) (*Session, error) {
	return nil, fmt.Errorf("tmux backend is not supported on Windows")
}

// AttachTmuxSession is unsupported on Windows.
func AttachTmuxSession(opts SessionOpts, tmuxName string, createdAt time.Time) (*Session, error) {
	return nil, fmt.Errorf("tmux backend is not supported on Windows")
}

// DiscoverTmuxSessions returns no sessions on Windows.
func DiscoverTmuxSessions(tmuxBinary string) ([]TmuxSessionMeta, error) {
	return nil, nil
}

// SubscribeOutput returns a per-subscriber output channel.
func (s *Session) SubscribeOutput() (uint64, <-chan []byte) {
	if s == nil {
		ch := make(chan []byte)
		close(ch)
		return 0, ch
	}
	ch := make(chan []byte, 1)
	id := s.nextSub.Add(1)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		close(ch)
		return id, ch
	}
	if s.subs == nil {
		s.subs = make(map[uint64]chan []byte)
	}
	s.subs[id] = ch
	s.mu.Unlock()
	return id, ch
}

// UnsubscribeOutput removes a subscriber channel.
func (s *Session) UnsubscribeOutput(id uint64) {
	if s == nil {
		return
	}
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

// Done returns a channel that is closed when the session's process exits.
func (s *Session) Done() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.done
}

// Write sends input to the PTY.
func (s *Session) Write(data []byte) (int, error) {
	if s == nil {
		return 0, fmt.Errorf("nil session")
	}
	return 0, fmt.Errorf("windows PTY backend not implemented yet (ConPTY required)")
}

// Resize changes the PTY window size.
func (s *Session) Resize(cols, rows uint16) error {
	if s == nil {
		return fmt.Errorf("nil session")
	}
	s.mu.Lock()
	s.Cols = cols
	s.Rows = rows
	s.mu.Unlock()
	return fmt.Errorf("windows PTY backend not implemented yet (ConPTY required)")
}

// IsRunning returns whether the shell process is still alive.
func (s *Session) IsRunning() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// Kill terminates the session.
func (s *Session) Kill() error {
	return s.ClosePreserve()
}

// ClosePreserve terminates wrapper resources (no-op for unsupported backend).
func (s *Session) ClosePreserve() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.running {
		s.running = false
		if s.done != nil {
			close(s.done)
		}
		for id, ch := range s.subs {
			delete(s.subs, id)
			close(ch)
		}
	}
	return nil
}
