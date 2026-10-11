package util

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBlockedDialIP(t *testing.T) {
	// Always blocked, regardless of the loopback allowance.
	alwaysBlocked := []string{"169.254.169.254", "169.254.0.1", "0.0.0.0", "0.1.2.3", "fe80::1"}
	for _, s := range alwaysBlocked {
		if !BlockedDialIP(net.ParseIP(s), true) {
			t.Errorf("%s should be blocked even with loopback allowed", s)
		}
	}
	// Loopback blocked by default, allowed only when permitted (dev mode).
	for _, s := range []string{"127.0.0.1", "::1"} {
		if !BlockedDialIP(net.ParseIP(s), false) {
			t.Errorf("%s should be blocked by default", s)
		}
		if BlockedDialIP(net.ParseIP(s), true) {
			t.Errorf("%s should be allowed when loopback permitted", s)
		}
	}
	// Private LAN ranges stay reachable: internal IdPs / guardrail services are legitimate.
	allowed := []string{"10.0.0.5", "192.168.1.10", "172.16.0.1", "8.8.8.8", "203.0.113.7"}
	for _, s := range allowed {
		if BlockedDialIP(net.ParseIP(s), false) {
			t.Errorf("%s should be allowed", s)
		}
	}
	if !BlockedDialIP(nil, true) {
		t.Error("nil IP should be blocked")
	}
}

func TestSafeHTTPClientBlocksLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	defer srv.Close()
	// The test server listens on 127.0.0.1, which the guard must refuse by default.
	if _, err := SafeHTTPClient(2*time.Second, false).Get(srv.URL); err == nil {
		t.Fatal("expected SSRF guard to block loopback request")
	}
	// With loopback allowed (dev mode) the same request succeeds.
	resp, err := SafeHTTPClient(2*time.Second, true).Get(srv.URL)
	if err != nil {
		t.Fatalf("loopback should be allowed in dev mode: %v", err)
	}
	resp.Body.Close()
}
