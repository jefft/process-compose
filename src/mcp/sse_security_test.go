package mcp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/f1bonacc1/process-compose/src/config"
	"github.com/f1bonacc1/process-compose/src/types"
)

func TestHostname(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:8081": "127.0.0.1",
		"127.0.0.1":      "127.0.0.1",
		"localhost:8081": "localhost",
		"localhost":      "localhost",
		"localhost.":     "localhost",
		"[::1]:8081":     "::1",
		"[::1]":          "::1",
		"":               "",
		"host.example":   "host.example",
	}
	for in, want := range cases {
		if got := hostname(in); got != want {
			t.Errorf("hostname(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHostAllowed(t *testing.T) {
	cases := []struct {
		host    string
		trusted []string
		want    bool
	}{
		{"127.0.0.1", nil, true},
		{"127.0.0.2", nil, true}, // whole 127.0.0.0/8 is loopback
		{"::1", nil, true},
		{"localhost", nil, true},
		{"LOCALHOST", nil, true},
		{"attacker.example", nil, false},
		{"", nil, false},
		{"192.168.1.5", nil, false},
		{"192.168.1.5", []string{"192.168.1.5"}, true},
		{"myhost", []string{"myhost"}, true},
		{"MyHost", []string{"myhost"}, true},
		{"attacker.example", []string{"*"}, true},
		{"", []string{"*"}, true},
	}
	for _, c := range cases {
		if got := hostAllowed(c.host, c.trusted); got != c.want {
			t.Errorf("hostAllowed(%q, %v) = %v, want %v", c.host, c.trusted, got, c.want)
		}
	}
}

func TestIsWildcardHost(t *testing.T) {
	cases := map[string]bool{
		"0.0.0.0":   true,
		"::":        true,
		"127.0.0.1": false,
		"localhost": false,
		"":          false,
	}
	for in, want := range cases {
		if got := isWildcardHost(in); got != want {
			t.Errorf("isWildcardHost(%q) = %v, want %v", in, got, want)
		}
	}
}

// runMiddleware sends a request through the SSE security middleware and returns
// the recorded response. The terminal handler asserts that the ResponseWriter
// still implements http.Flusher, guarding against a regression that would break
// SSE streaming.
func runMiddleware(t *testing.T, s *Server, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := w.(http.Flusher); !ok {
			http.Error(w, "response writer is not an http.Flusher", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	s.sseSecurityMiddleware(next).ServeHTTP(rec, req)
	return rec
}

func newReq(host, origin string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/sse", nil)
	req.Host = host
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	return req
}

func TestSSESecurityMiddleware_HostAndOrigin(t *testing.T) {
	s := &Server{config: &types.MCPServerConfig{Host: "127.0.0.1", Port: 8081}}

	cases := []struct {
		name   string
		req    *http.Request
		status int
	}{
		{"loopback ipv4", newReq("127.0.0.1:8081", ""), http.StatusOK},
		{"loopback name", newReq("localhost:8081", ""), http.StatusOK},
		{"loopback ipv6", newReq("[::1]:8081", ""), http.StatusOK},
		{"forged host", newReq("attacker.example:8081", ""), http.StatusForbidden},
		{"loopback host, loopback origin", newReq("127.0.0.1:8081", "http://127.0.0.1:8081"), http.StatusOK},
		{"loopback host, untrusted origin", newReq("127.0.0.1:8081", "http://attacker.example:8081"), http.StatusForbidden},
		{"loopback host, null origin", newReq("127.0.0.1:8081", "null"), http.StatusForbidden},
		{"no host", newReq("", ""), http.StatusForbidden},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := runMiddleware(t, s, c.req).Code; got != c.status {
				t.Errorf("status = %d, want %d", got, c.status)
			}
		})
	}
}

func TestSSESecurityMiddleware_TrustedHosts(t *testing.T) {
	s := &Server{config: &types.MCPServerConfig{Host: "0.0.0.0", Port: 8081, TrustedHosts: []string{"myhost"}}}

	if got := runMiddleware(t, s, newReq("myhost:8081", "")).Code; got != http.StatusOK {
		t.Errorf("trusted host: status = %d, want %d", got, http.StatusOK)
	}
	if got := runMiddleware(t, s, newReq("other.example:8081", "")).Code; got != http.StatusForbidden {
		t.Errorf("untrusted host: status = %d, want %d", got, http.StatusForbidden)
	}

	// "*" opt-out trusts everything, including a forged host.
	sAny := &Server{config: &types.MCPServerConfig{Host: "0.0.0.0", Port: 8081, TrustedHosts: []string{"*"}}}
	if got := runMiddleware(t, sAny, newReq("attacker.example:8081", "http://attacker.example:8081")).Code; got != http.StatusOK {
		t.Errorf("wildcard opt-out: status = %d, want %d", got, http.StatusOK)
	}
}

func TestSSESecurityMiddleware_Token(t *testing.T) {
	const token = "super-secret-token-1234567890"
	t.Setenv(config.EnvVarApiToken, token)
	s := &Server{config: &types.MCPServerConfig{Host: "127.0.0.1", Port: 8081}}

	// Loopback host but no/wrong token -> 401.
	if got := runMiddleware(t, s, newReq("127.0.0.1:8081", "")).Code; got != http.StatusUnauthorized {
		t.Errorf("missing token: status = %d, want %d", got, http.StatusUnauthorized)
	}
	wrong := newReq("127.0.0.1:8081", "")
	wrong.Header.Set(config.TokenHeader, "nope")
	if got := runMiddleware(t, s, wrong).Code; got != http.StatusUnauthorized {
		t.Errorf("wrong token: status = %d, want %d", got, http.StatusUnauthorized)
	}

	// Correct token via X-PC-Token-Key.
	viaHeader := newReq("127.0.0.1:8081", "")
	viaHeader.Header.Set(config.TokenHeader, token)
	if got := runMiddleware(t, s, viaHeader).Code; got != http.StatusOK {
		t.Errorf("token header: status = %d, want %d", got, http.StatusOK)
	}

	// Correct token via Authorization: Bearer.
	viaBearer := newReq("127.0.0.1:8081", "")
	viaBearer.Header.Set("Authorization", "Bearer "+token)
	if got := runMiddleware(t, s, viaBearer).Code; got != http.StatusOK {
		t.Errorf("bearer token: status = %d, want %d", got, http.StatusOK)
	}

	// Scheme name is case-insensitive (RFC 7235).
	viaLowerBearer := newReq("127.0.0.1:8081", "")
	viaLowerBearer.Header.Set("Authorization", "bearer "+token)
	if got := runMiddleware(t, s, viaLowerBearer).Code; got != http.StatusOK {
		t.Errorf("lowercase bearer token: status = %d, want %d", got, http.StatusOK)
	}

	// Host validation still runs before the token check.
	forged := newReq("attacker.example:8081", "")
	forged.Header.Set(config.TokenHeader, token)
	if got := runMiddleware(t, s, forged).Code; got != http.StatusForbidden {
		t.Errorf("forged host with valid token: status = %d, want %d", got, http.StatusForbidden)
	}
}
