package session

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/creack/pty"
)

// Session represents a single PTY terminal session.
type Session struct {
	ID        string
	Name      string
	CreatedAt time.Time
	Cols      uint16
	Rows      uint16

	cmd     *exec.Cmd
	ptmx    *os.File
	output  chan []byte   // buffered channel for read -> write goroutine
	done    chan struct{} // closed when PTY process exits
	mu      sync.Mutex
	closed  bool
	running bool

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
	Cols       uint16
	Rows       uint16
	BufSize    int // PTY read buffer (default 16384)
	ChanSize   int // Output channel slots (default 64)
	ThrottleMB int // Throttle threshold in MB (default 1)
}

// NewSession creates and starts a new PTY session.
func NewSession(opts SessionOpts) (*Session, error) {
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

	cmd := exec.Command(opts.Shell)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	winSize := &pty.Winsize{Cols: opts.Cols, Rows: opts.Rows}
	ptmx, err := pty.StartWithSize(cmd, winSize)
	if err != nil {
		return nil, fmt.Errorf("starting pty: %w", err)
	}

	s := &Session{
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
		bufSize:    opts.BufSize,
		chanSize:   opts.ChanSize,
		throttleMB: opts.ThrottleMB,
	}

	// Start PTY reader goroutine
	go s.readLoop()

	// Start process waiter goroutine
	go s.waitLoop()

	return s, nil
}

// readLoop reads from the PTY and pushes to the output channel.
// If the channel is full (backpressure), it drops data and logs.
func (s *Session) readLoop() {
	buf := make([]byte, s.bufSize)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])
			s.BytesRead += int64(n)

			// Non-blocking send with backpressure handling
			select {
			case s.output <- data:
				// Sent successfully
			default:
				// Channel full — backpressure. Drop this chunk.
				// The consumer (WebSocket writer) is slower than the producer (PTY).
				// This prevents unbounded memory growth.
			}
		}
		if err != nil {
			if err != io.EOF {
				// Unexpected error, but we still close cleanly
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

// Output returns the channel that receives PTY output chunks.
// The consumer should read from this until the channel is closed.
func (s *Session) Output() <-chan []byte {
	return s.output
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

// Resize changes the PTY window size. This triggers tmux/shell reflow.
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
	return nil
}

// IsRunning returns whether the shell process is still alive.
func (s *Session) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// Kill terminates the session.
func (s *Session) Kill() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Signal(syscall.SIGTERM)
		// Give it 2 seconds to exit gracefully, then SIGKILL
		go func() {
			select {
			case <-s.done:
			case <-time.After(2 * time.Second):
				if s.cmd.Process != nil {
					_ = s.cmd.Process.Kill()
				}
			}
		}()
	}
	return s.ptmx.Close()
}
