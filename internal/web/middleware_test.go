package web

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/mmrzaf/gitman/internal/config"
)

func TestResolveClientIP(t *testing.T) {
	a := &App{cfg: &config.Config{Retention: config.DefaultRetention(), TrustedProxies: []netip.Prefix{
		netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("10.0.0.1/32"),
	}}}
	cases := []struct {
		name   string
		remote string
		xff    []string
		want   string
	}{
		{"direct client", "203.0.113.9:5000", nil, "203.0.113.9"},
		{"untrusted peer's header is ignored", "203.0.113.9:5000", []string{"1.2.3.4"}, "203.0.113.9"},
		{"trusted proxy", "172.18.0.5:4000", []string{"198.51.100.7"}, "198.51.100.7"},
		{"spoofed left entries are ignored", "172.18.0.5:4000", []string{"6.6.6.6, 198.51.100.7"}, "198.51.100.7"},
		{"chain of trusted proxies", "172.18.0.5:4000", []string{"198.51.100.7, 10.0.0.1"}, "198.51.100.7"},
		{"multiple headers", "172.18.0.5:4000", []string{"6.6.6.6", "198.51.100.7"}, "198.51.100.7"},
		{"garbage stops the walk", "172.18.0.5:4000", []string{"198.51.100.7, garbage"}, "172.18.0.5"},
		{"trusted proxy without header", "172.18.0.5:4000", nil, "172.18.0.5"},
		{"ipv4-mapped peer", "[::ffff:172.18.0.5]:4000", []string{"198.51.100.7"}, "198.51.100.7"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = c.remote
			for _, h := range c.xff {
				req.Header.Add("X-Forwarded-For", h)
			}
			if got := a.resolveClientIP(req); got != c.want {
				t.Errorf("resolveClientIP = %q, want %q", got, c.want)
			}
		})
	}
}

func TestClientIPWithoutMiddleware(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.0.2.1:1234"
	if got := clientIP(req); got != "192.0.2.1" {
		t.Errorf("clientIP = %q", got)
	}
}
