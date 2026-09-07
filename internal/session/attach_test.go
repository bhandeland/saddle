package session

import (
	"net"
	"strings"
	"testing"
)

func TestProxyPortFreeAcceptsAnUnusedAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // now nothing holds it

	if err := proxyPortFree(addr); err != nil {
		t.Fatalf("proxyPortFree(%s) = %v, want nil", addr, err)
	}
}

// Two saddle processes attached to one session would share a proxy without
// either knowing. Refusing beats silently sharing containment.
func TestProxyPortFreeRefusesAnAddressSomethingElseHolds(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	addr := ln.Addr().String()

	err = proxyPortFree(addr)
	if err == nil {
		t.Fatal("proxyPortFree accepted an address already in use")
	}
	if !strings.Contains(err.Error(), addr) {
		t.Fatalf("error does not name the address: %v", err)
	}
	if !strings.Contains(err.Error(), "already attached") {
		t.Fatalf("error does not explain the likely cause: %v", err)
	}
}
