// Package egress is a host-side CONNECT proxy that permits only allowlisted
// hosts. It is the portable half of saddle's containment: the container is
// placed on a network with no route to the internet, and this proxy is the
// only way out. It must not import any runtime-specific package.
package egress

import (
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
)

type Proxy struct {
	allow map[string]bool
	srv   *http.Server
	once  sync.Once
}

// New builds a proxy permitting exactly the given hostnames. Matching is on
// the hostname only: exact, case-insensitive, port ignored. There is no
// wildcard or suffix matching, so "anthropic.com" does not permit
// "evil.anthropic.com".
func New(allow []string) *Proxy {
	m := make(map[string]bool, len(allow))
	for _, h := range allow {
		m[strings.ToLower(strings.TrimSpace(h))] = true
	}
	return &Proxy{allow: m}
}

// Allowed reports whether a "host" or "host:port" target is permitted.
func (p *Proxy) Allowed(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	return p.allow[strings.ToLower(host)]
}

// Listen binds the proxy and serves in the background, returning the bound
// address. Pass ":0" or "host:0" to get an arbitrary free port.
func (p *Proxy) Listen(addr string) (string, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", err
	}
	p.srv = &http.Server{Handler: http.HandlerFunc(p.handle)}
	go func() { _ = p.srv.Serve(ln) }()
	return ln.Addr().String(), nil
}

func (p *Proxy) Close() error {
	var err error
	p.once.Do(func() {
		if p.srv != nil {
			err = p.srv.Close()
		}
	})
	return err
}

func (p *Proxy) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		http.Error(w, "saddle egress proxy accepts CONNECT only", http.StatusMethodNotAllowed)
		return
	}
	if !p.Allowed(r.Host) {
		http.Error(w, "blocked by saddle egress allowlist", http.StatusForbidden)
		return
	}
	upstream, err := net.Dial("tcp", r.Host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		_ = upstream.Close()
		http.Error(w, "hijacking unsupported", http.StatusInternalServerError)
		return
	}
	client, _, err := hj.Hijack()
	if err != nil {
		_ = upstream.Close()
		return
	}
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		_ = client.Close()
		_ = upstream.Close()
		return
	}
	go func() {
		defer func() { _ = upstream.Close() }()
		_, _ = io.Copy(upstream, client)
	}()
	go func() {
		defer func() { _ = client.Close() }()
		_, _ = io.Copy(client, upstream)
	}()
}
