package server

import (
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestClientAddr(t *testing.T) {
	s := &Server{Deps: Deps{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("172.30.0.0/24")}}}
	for _, tc := range []struct {
		name, peer string
		xff        []string
		want       string
	}{
		{"no proxy", "192.168.1.20:5000", nil, "192.168.1.20"},
		{"untrusted peer can't forge", "192.168.1.20:5000", []string{"10.9.9.9"}, "192.168.1.20"},
		{"trusted proxy", "172.30.0.2:4000", []string{"192.168.1.30"}, "192.168.1.30"},
		{"rightmost untrusted hop", "172.30.0.2:4000", []string{"6.6.6.6, 192.168.1.30, 172.30.0.9"}, "192.168.1.30"},
		{"several headers", "172.30.0.2:4000", []string{"6.6.6.6", "192.168.1.31"}, "192.168.1.31"},
		{"trusted proxy, no header", "172.30.0.2:4000", nil, "172.30.0.2"},
		{"mangled hop", "172.30.0.2:4000", []string{"192.168.1.30, nonsense"}, "172.30.0.2"},
		{"ipv4-mapped", "[::ffff:172.30.0.2]:4000", []string{"192.168.1.32"}, "192.168.1.32"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequestWithContext(t.Context(), "GET", "/", nil)
			r.RemoteAddr = tc.peer
			for _, v := range tc.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			if got := s.clientAddr(r); got != tc.want {
				t.Errorf("clientAddr = %q, want %q", got, tc.want)
			}
		})
	}
}
