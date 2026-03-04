package server

import (
	"net/http"
	"testing"

	"github.com/cosmobean/runic/internal/config"
)

func TestNormalizeOrigin(t *testing.T) {
	tests := []struct {
		in   string
		out  string
		good bool
	}{
		{in: "https://terminal.example.com", out: "https://terminal.example.com", good: true},
		{in: "HTTPS://Terminal.Example.com/", out: "https://terminal.example.com", good: true},
		{in: "invalid", out: "", good: false},
	}

	for _, tc := range tests {
		got, ok := normalizeOrigin(tc.in)
		if ok != tc.good {
			t.Fatalf("normalizeOrigin(%q) ok=%v want %v", tc.in, ok, tc.good)
		}
		if got != tc.out {
			t.Fatalf("normalizeOrigin(%q)=%q want %q", tc.in, got, tc.out)
		}
	}
}

func TestCheckOrigin(t *testing.T) {
	s := &Server{
		allowedOrigins: map[string]struct{}{
			"https://terminal.example.com": {},
		},
	}
	r, _ := http.NewRequest(http.MethodGet, "http://localhost/ws", nil)
	r.Header.Set("Origin", "https://terminal.example.com")
	if !s.checkOrigin(r) {
		t.Fatal("expected origin to be allowed")
	}

	r.Header.Set("Origin", "https://evil.example.com")
	if s.checkOrigin(r) {
		t.Fatal("expected origin to be rejected")
	}
}

func TestClientIPTrustedProxy(t *testing.T) {
	s := &Server{
		cfg: &config.Config{
			Security: config.SecurityConfig{
				TrustProxyHeaders: true,
			},
		},
		trustedProxyNet: parseCIDRs([]string{"127.0.0.1/32"}),
	}
	r, _ := http.NewRequest(http.MethodGet, "http://localhost/ws", nil)
	r.RemoteAddr = "127.0.0.1:34567"
	r.Header.Set("X-Forwarded-For", "198.51.100.9")
	if got := s.clientIP(r); got != "198.51.100.9" {
		t.Fatalf("clientIP trusted proxy got %q", got)
	}
}

func TestClientIPUntrustedProxy(t *testing.T) {
	s := &Server{
		cfg: &config.Config{
			Security: config.SecurityConfig{
				TrustProxyHeaders: true,
			},
		},
		trustedProxyNet: parseCIDRs([]string{"127.0.0.1/32"}),
	}
	r, _ := http.NewRequest(http.MethodGet, "http://localhost/ws", nil)
	r.RemoteAddr = "198.51.100.20:12345"
	r.Header.Set("X-Forwarded-For", "203.0.113.10")
	if got := s.clientIP(r); got != "198.51.100.20" {
		t.Fatalf("clientIP untrusted proxy got %q", got)
	}
}
