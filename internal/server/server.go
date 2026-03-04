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
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/cosmobean/runic/internal/auth"
	"github.com/cosmobean/runic/internal/config"
	"github.com/cosmobean/runic/internal/protocol"
	"github.com/cosmobean/runic/internal/session"
)

//go:embed all:web
var webFS embed.FS

type Server struct {
	cfg             *config.Config
	auth            *auth.Authenticator
	mgr             *session.Manager
	httpSrv         *http.Server
	upgrader        websocket.Upgrader
	allowedOrigins  map[string]struct{}
	trustedProxyNet []*net.IPNet
}

var wsConnID atomic.Uint64

func New(cfg *config.Config) *Server {
	mgr := session.NewManager(session.ManagerOpts{
		DefaultShell: cfg.Sessions.DefaultShell,
		SessionMode:  cfg.Sessions.SessionMode,
		LoginShell:   cfg.Sessions.LoginShell,
		StartDir:     cfg.Sessions.StartDir,
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

	srv := &Server{
		cfg:            cfg,
		auth:           authenticator,
		mgr:            mgr,
		allowedOrigins: make(map[string]struct{}),
	}
	srv.upgrader = websocket.Upgrader{
		ReadBufferSize:  16384,
		WriteBufferSize: 16384,
		CheckOrigin:     srv.checkOrigin,
	}
	for _, raw := range cfg.Security.AllowedOrigins {
		if strings.TrimSpace(raw) == "*" {
			srv.allowedOrigins["*"] = struct{}{}
			continue
		}
		if norm, ok := normalizeOrigin(raw); ok {
			srv.allowedOrigins[norm] = struct{}{}
		}
	}
	srv.trustedProxyNet = parseCIDRs(cfg.Security.TrustedProxyCIDRs)

	return srv
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
	if len(s.allowedOrigins) == 0 {
		log.Printf("Security warning: security.allowed_origins is empty; WebSocket origin checks are permissive")
	} else {
		origins := make([]string, 0, len(s.allowedOrigins))
		for origin := range s.allowedOrigins {
			origins = append(origins, origin)
		}
		sort.Strings(origins)
		log.Printf("WebSocket allowed origins: %s", strings.Join(origins, ", "))
	}

	// Graceful shutdown
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.mgr.ShutdownAll()
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
	connID := wsConnID.Add(1)
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws_upgrade_failed conn=%d remote=%q origin=%q err=%v", connID, r.RemoteAddr, r.Header.Get("Origin"), err)
		return
	}
	defer conn.Close()
	log.Printf("ws_connected conn=%d remote=%q origin=%q", connID, r.RemoteAddr, r.Header.Get("Origin"))
	defer log.Printf("ws_disconnected conn=%d", connID)

	// Set TCP_NODELAY for low latency
	if tcpConn, ok := conn.UnderlyingConn().(*net.TCPConn); ok {
		_ = tcpConn.SetNoDelay(true)
	} else if tlsConn, ok := conn.UnderlyingConn().(*tls.Conn); ok {
		if tcpConn, ok := tlsConn.NetConn().(*net.TCPConn); ok {
			_ = tcpConn.SetNoDelay(true)
		}
	}

	clientIP := s.clientIP(r)

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
		log.Printf("auth_failed conn=%d ip=%q code=AUTH_REQUIRED reason=%q", connID, clientIP, "first message must be auth")
		return
	}

	authReq, err := protocol.DecodeData[protocol.AuthRequest](msg)
	if err != nil {
		sendError(conn, "AUTH_INVALID", "Invalid auth message")
		log.Printf("auth_failed conn=%d ip=%q code=AUTH_INVALID reason=%q", connID, clientIP, "invalid auth message")
		return
	}

	if s.cfg.Auth.RequireToken {
		if err := s.auth.Verify(authReq.Token, clientIP); err != nil {
			sendError(conn, "AUTH_FAILED", err.Error())
			log.Printf("auth_failed conn=%d ip=%q code=AUTH_FAILED reason=%q", connID, clientIP, err.Error())
			return
		}
	}
	log.Printf("auth_ok conn=%d ip=%q client_id=%q", connID, clientIP, authReq.ClientID)

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
	subID, out := sess.SubscribeOutput()
	defer sess.UnsubscribeOutput(subID)

	for {
		select {
		case <-ctx.Done():
			return
		case data, ok := <-out:
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

func (s *Server) checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // Non-browser clients often omit Origin.
	}
	if len(s.allowedOrigins) == 0 {
		return true
	}
	if _, ok := s.allowedOrigins["*"]; ok {
		return true
	}
	norm, ok := normalizeOrigin(origin)
	if !ok {
		return false
	}
	if _, allowed := s.allowedOrigins[norm]; allowed {
		return true
	}
	return false
}

func normalizeOrigin(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", false
	}
	return strings.ToLower(u.Scheme + "://" + u.Host), true
}

func parseCIDRs(values []string) []*net.IPNet {
	list := make([]*net.IPNet, 0, len(values))
	for _, raw := range values {
		_, cidr, err := net.ParseCIDR(strings.TrimSpace(raw))
		if err == nil && cidr != nil {
			list = append(list, cidr)
		}
	}
	return list
}

func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if !s.cfg.Security.TrustProxyHeaders || ip == nil || !s.isTrustedProxy(ip) {
		return host
	}

	fwd := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
	if fwd == "" {
		return host
	}
	parts := strings.Split(fwd, ",")
	candidate := strings.TrimSpace(parts[0])
	if net.ParseIP(candidate) == nil {
		return host
	}
	return candidate
}

func (s *Server) isTrustedProxy(ip net.IP) bool {
	for _, cidr := range s.trustedProxyNet {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}
