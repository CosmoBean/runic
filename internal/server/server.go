package server

import (
	"context"
	"crypto/tls"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/cosmobean/runic/internal/auth"
	"github.com/cosmobean/runic/internal/config"
	"github.com/cosmobean/runic/internal/protocol"
	"github.com/cosmobean/runic/internal/session"
)

//go:embed all:web
var webFS embed.FS

var upgrader = websocket.Upgrader{
	ReadBufferSize:  16384,
	WriteBufferSize: 16384,
	CheckOrigin:     func(r *http.Request) bool { return true }, // Allow all origins for self-hosted
}

type Server struct {
	cfg     *config.Config
	auth    *auth.Authenticator
	mgr     *session.Manager
	httpSrv *http.Server
}

func New(cfg *config.Config) *Server {
	mgr := session.NewManager(session.ManagerOpts{
		DefaultShell: cfg.Sessions.DefaultShell,
		MaxSessions:  cfg.Sessions.MaxSessions,
		BufSize:      cfg.Sessions.PtyBufferSize,
		ChanSize:     cfg.Sessions.WsWriteBuffer,
		ThrottleMB:   cfg.Sessions.ThrottleMB,
	})

	authenticator := auth.New(
		cfg.Auth.TokenHash,
		cfg.Auth.RateLimit,
		cfg.Auth.LockoutMin,
	)

	return &Server{
		cfg:  cfg,
		auth: authenticator,
		mgr:  mgr,
	}
}

func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()

	// WebSocket endpoint
	mux.HandleFunc("/ws", s.handleWebSocket)

	// Health check
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// Serve embedded web UI
	webContent, err := fs.Sub(webFS, "web")
	if err != nil {
		// If embed fails, fall back to no web UI
		log.Println("Warning: could not load embedded web UI:", err)
	} else {
		mux.Handle("/", http.FileServer(http.FS(webContent)))
	}

	addr := fmt.Sprintf("%s:%d", s.cfg.Server.Host, s.cfg.Server.Port)
	s.httpSrv = &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	// TLS setup
	if s.cfg.TLS.Mode != "none" {
		tlsCfg, err := s.loadTLS()
		if err != nil {
			return fmt.Errorf("tls: %w", err)
		}
		ln = tls.NewListener(ln, tlsCfg)
		log.Printf("Runic listening on https://%s", addr)
	} else {
		log.Printf("Runic listening on http://%s (WARNING: no TLS)", addr)
	}
	log.Printf("Machine name: %s", s.cfg.Machine.Name)

	// Graceful shutdown
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.mgr.KillAll()
		_ = s.httpSrv.Shutdown(shutdownCtx)
	}()

	if err := s.httpSrv.Serve(ln); err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *Server) loadTLS() (*tls.Config, error) {
	switch s.cfg.TLS.Mode {
	case "custom":
		cert, err := tls.LoadX509KeyPair(s.cfg.TLS.Cert, s.cfg.TLS.Key)
		if err != nil {
			return nil, err
		}
		return &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}, nil
	case "self-signed":
		cert, err := generateSelfSignedCert()
		if err != nil {
			return nil, err
		}
		return &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}, nil
	default:
		return nil, fmt.Errorf("unknown TLS mode: %s", s.cfg.TLS.Mode)
	}
}

// handleWebSocket manages the lifecycle of a single client connection.
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Upgrade error: %v", err)
		return
	}
	defer conn.Close()

	// Set TCP_NODELAY for low latency
	if tcpConn, ok := conn.UnderlyingConn().(*net.TCPConn); ok {
		_ = tcpConn.SetNoDelay(true)
	} else if tlsConn, ok := conn.UnderlyingConn().(*tls.Conn); ok {
		if tcpConn, ok := tlsConn.NetConn().(*net.TCPConn); ok {
			_ = tcpConn.SetNoDelay(true)
		}
	}

	clientIP := r.RemoteAddr
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		clientIP = strings.Split(fwd, ",")[0]
	}

	// Step 1: Authenticate
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{}) // Clear deadline after auth

	msg, err := protocol.Decode(raw)
	if err != nil || msg.Type != protocol.TypeAuth {
		sendError(conn, "AUTH_REQUIRED", "First message must be auth")
		return
	}

	authReq, err := protocol.DecodeData[protocol.AuthRequest](msg)
	if err != nil {
		sendError(conn, "AUTH_INVALID", "Invalid auth message")
		return
	}

	if err := s.auth.Verify(authReq.Token, clientIP); err != nil {
		sendError(conn, "AUTH_FAILED", err.Error())
		return
	}

	// Auth succeeded
	okData, _ := protocol.Encode(protocol.TypeAuthOK, protocol.AuthOKResponse{
		MachineName: s.cfg.Machine.Name,
		Version:     "0.1.0",
	})
	_ = conn.WriteMessage(websocket.TextMessage, okData)

	// Step 2: Message loop
	var attachedSession *session.Session
	var outputCancel context.CancelFunc
	var writeMu sync.Mutex

	safeSend := func(msgType int, data []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteMessage(msgType, data)
	}

	for {
		msgType, raw, err := conn.ReadMessage()
		if err != nil {
			break
		}

		// Binary frame = terminal I/O
		if msgType == websocket.BinaryMessage && len(raw) > 0 {
			if attachedSession == nil {
				continue
			}
			switch raw[0] {
			case protocol.FrameInput:
				_, _ = attachedSession.Write(raw[1:])
			case protocol.FrameResize:
				if len(raw) >= 5 {
					cols, rows := protocol.DecodeResize(raw[1:])
					_ = attachedSession.Resize(cols, rows)
				}
			}
			continue
		}

		// Text frame = control message
		msg, err := protocol.Decode(raw)
		if err != nil {
			continue
		}

		switch msg.Type {
		case protocol.TypeListSessions:
			resp, _ := protocol.Encode(protocol.TypeSessionList, protocol.SessionListResponse{
				Sessions: s.mgr.List(),
			})
			_ = safeSend(websocket.TextMessage, resp)

		case protocol.TypeCreate:
			req, err := protocol.DecodeData[protocol.CreateRequest](msg)
			if err != nil {
				sendError(conn, "INVALID", "Invalid create request")
				continue
			}
			sess, err := s.mgr.Create(*req)
			if err != nil {
				sendError(conn, "CREATE_FAILED", err.Error())
				continue
			}
			resp, _ := protocol.Encode(protocol.TypeAttached, protocol.AttachedResponse{
				SessionID: sess.ID,
				Cols:      int(sess.Cols),
				Rows:      int(sess.Rows),
			})
			_ = safeSend(websocket.TextMessage, resp)

			// Auto-attach to newly created session
			if outputCancel != nil {
				outputCancel()
			}
			attachedSession = sess
			var outputCtx context.Context
			outputCtx, outputCancel = context.WithCancel(context.Background())
			go streamOutput(outputCtx, sess, safeSend)

		case protocol.TypeAttach:
			req, err := protocol.DecodeData[protocol.AttachRequest](msg)
			if err != nil {
				continue
			}
			sess, ok := s.mgr.Get(req.SessionID)
			if !ok {
				sendError(conn, "NOT_FOUND", "Session not found")
				continue
			}

			// Detach from previous
			if outputCancel != nil {
				outputCancel()
			}
			attachedSession = sess
			var outputCtx context.Context
			outputCtx, outputCancel = context.WithCancel(context.Background())

			resp, _ := protocol.Encode(protocol.TypeAttached, protocol.AttachedResponse{
				SessionID: sess.ID,
				Cols:      int(sess.Cols),
				Rows:      int(sess.Rows),
			})
			_ = safeSend(websocket.TextMessage, resp)
			go streamOutput(outputCtx, sess, safeSend)

		case protocol.TypeDetach:
			if outputCancel != nil {
				outputCancel()
				outputCancel = nil
			}
			attachedSession = nil
			resp, _ := protocol.Encode(protocol.TypeDetached, map[string]string{"reason": "user"})
			_ = safeSend(websocket.TextMessage, resp)

		case protocol.TypeKill:
			req, err := protocol.DecodeData[protocol.KillRequest](msg)
			if err != nil {
				continue
			}
			if attachedSession != nil && attachedSession.ID == req.SessionID {
				if outputCancel != nil {
					outputCancel()
					outputCancel = nil
				}
				attachedSession = nil
			}
			if err := s.mgr.Kill(req.SessionID); err != nil {
				sendError(conn, "KILL_FAILED", err.Error())
			}

		case protocol.TypePing:
			resp, _ := protocol.Encode(protocol.TypePong, nil)
			_ = safeSend(websocket.TextMessage, resp)
		}
	}

	// Cleanup on disconnect
	if outputCancel != nil {
		outputCancel()
	}
}

// streamOutput reads from a session's output channel and sends to the WebSocket.
func streamOutput(ctx context.Context, sess *session.Session, send func(int, []byte) error) {
	for {
		select {
		case <-ctx.Done():
			return
		case data, ok := <-sess.Output():
			if !ok {
				// Session ended
				resp, _ := protocol.Encode(protocol.TypeDetached, map[string]string{"reason": "exited"})
				_ = send(websocket.TextMessage, resp)
				return
			}
			frame := protocol.EncodeBinaryFrame(protocol.FrameOutput, data)
			if err := send(websocket.BinaryMessage, frame); err != nil {
				return // WebSocket write failed, client disconnected
			}
		}
	}
}

func sendError(conn *websocket.Conn, code, message string) {
	data, _ := protocol.Encode(protocol.TypeError, protocol.ErrorResponse{Code: code, Message: message})
	_ = conn.WriteMessage(websocket.TextMessage, data)
}
