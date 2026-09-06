package egress

import (
	"net"
	"testing"
)

func TestAllowedExactHostCaseInsensitive(t *testing.T) {
	p := New([]string{"api.anthropic.com"})
	for _, in := range []string{"api.anthropic.com:443", "API.Anthropic.COM:443", "api.anthropic.com"} {
		if !p.Allowed(in) {
			t.Errorf("Allowed(%q) = false, want true", in)
		}
	}
}

func TestDeniedByDefault(t *testing.T) {
	p := New([]string{"api.anthropic.com"})
	for _, in := range []string{"example.com:443", "evil.api.anthropic.com:443", "anthropic.com:443"} {
		if p.Allowed(in) {
			t.Errorf("Allowed(%q) = true, want false", in)
		}
	}
}

func TestEmptyAllowlistDeniesEverything(t *testing.T) {
	p := New(nil)
	if p.Allowed("api.anthropic.com:443") {
		t.Fatal("empty allowlist must deny")
	}
}

func TestListenReturnsBoundAddress(t *testing.T) {
	p := New([]string{"api.anthropic.com"})
	addr, err := p.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer p.Close()
	if _, _, err := net.SplitHostPort(addr); err != nil {
		t.Fatalf("Listen returned %q, not host:port: %v", addr, err)
	}
}

func TestConnectToDeniedHostIsRefused(t *testing.T) {
	p := New([]string{"allowed.example"})
	addr, err := p.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("CONNECT denied.example:443 HTTP/1.1\r\nHost: denied.example:443\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	got := string(buf[:n])
	if len(got) < 12 || got[9:12] != "403" {
		t.Fatalf("got %q, want a 403 status line", got)
	}
}
