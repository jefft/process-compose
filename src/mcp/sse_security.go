package mcp

import (
	"crypto/subtle"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/f1bonacc1/process-compose/src/config"
	"github.com/rs/zerolog/log"
)

// sseSecurityMiddleware guards the MCP SSE listener against browser-driven
// attacks (notably DNS rebinding) before any request reaches the MCP dispatch.
//
// Checks run in order and fail closed:
//  1. Host allowlist  - the primary DNS-rebinding defense. A browser cannot
//     forge the Host header to a loopback name, so only loopback (or explicitly
//     trusted) hosts are accepted.
//  2. Origin allowlist - defense-in-depth for cross-origin browser requests.
//  3. Optional token   - when PC_API_TOKEN is configured, require it via
//     X-PC-Token-Key or Authorization: Bearer.
//
// The original http.ResponseWriter is passed straight through: mcp-go asserts
// w.(http.Flusher) directly, so wrapping the writer would break SSE streaming.
func (s *Server) sseSecurityMiddleware(next http.Handler) http.Handler {
	trusted := s.trustedHosts()
	token := config.GetApiToken()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. Host validation (use r.Host only; never trust X-Forwarded-Host).
		if !hostAllowed(hostname(r.Host), trusted) {
			reject(w, r, http.StatusForbidden, "untrusted host")
			return
		}

		// 2. Origin validation, only when the header is present.
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || !hostAllowed(hostname(u.Host), trusted) {
				reject(w, r, http.StatusForbidden, "untrusted origin")
				return
			}
		}

		// 3. Optional token authentication.
		if token != "" && !tokenValid(r, token) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			reject(w, r, http.StatusUnauthorized, "invalid or missing token")
			return
		}

		next.ServeHTTP(w, r)
	})
}

// trustedHosts is the set of extra host names accepted in addition to loopback.
// It includes the operator-configured trusted_hosts plus the configured bind
// host when that host is a concrete name/IP. A wildcard bind (0.0.0.0 / ::) is
// deliberately excluded - treating "bound to all interfaces" as "trust all
// hosts" would reopen the vulnerability.
func (s *Server) trustedHosts() []string {
	trusted := make([]string, 0, len(s.config.TrustedHosts)+1)
	trusted = append(trusted, s.config.TrustedHosts...)
	if h := s.config.Host; h != "" && !isWildcardHost(h) {
		trusted = append(trusted, h)
	}
	return trusted
}

// warnInsecureSSE logs prominent warnings for SSE configurations that reduce
// the effectiveness of the trust boundary, without refusing to start.
func (s *Server) warnInsecureSSE() {
	if s.config.ExposeControlTools && config.GetApiToken() == "" {
		log.Warn().Msg("MCP SSE: expose_control_tools is enabled without an auth token; " +
			"Host/Origin validation still blocks browser DNS-rebinding, but set " +
			config.EnvVarApiToken + " to require authentication for MCP clients")
	}
	if isWildcardHost(s.config.Host) && len(s.config.TrustedHosts) == 0 {
		log.Warn().Str("host", s.config.Host).Msg("MCP SSE: bound to a wildcard address with no trusted_hosts; " +
			"non-loopback clients will be rejected. Add their host name/IP to mcp_server.trusted_hosts")
	}
}

// hostname extracts the bare host from a "host" or "host:port" value, stripping
// any port, IPv6 brackets, and a trailing dot so it can be matched robustly.
func hostname(hostport string) string {
	h := hostport
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	} else {
		h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
	}
	return strings.TrimSuffix(h, ".")
}

// hostAllowed reports whether h is loopback, an explicitly trusted name, or the
// "*" wildcard opt-out. It fails closed on an empty host.
func hostAllowed(h string, trusted []string) bool {
	if slices.Contains(trusted, "*") { // explicit, documented opt-out
		return true
	}
	if h == "" {
		return false
	}
	if ip := net.ParseIP(h); ip != nil {
		if ip.IsLoopback() { // covers 127.0.0.0/8 and ::1
			return true
		}
	} else if strings.EqualFold(h, "localhost") {
		return true
	}
	for _, t := range trusted {
		if strings.EqualFold(h, t) {
			return true
		}
	}
	return false
}

// isWildcardHost reports whether h is an unspecified/wildcard bind address such
// as 0.0.0.0 or ::.
func isWildcardHost(h string) bool {
	if ip := net.ParseIP(hostname(h)); ip != nil {
		return ip.IsUnspecified()
	}
	return false
}

// bearerPrefix is the (case-insensitive) Authorization scheme prefix accepted
// for MCP SSE token auth.
const bearerPrefix = "Bearer "

// tokenValid checks the request auth token against the configured token using a
// constant-time comparison. The token may be supplied via the X-PC-Token-Key
// header or an Authorization: Bearer header (used by MCP clients).
func tokenValid(r *http.Request, token string) bool {
	provided := r.Header.Get(config.TokenHeader)
	if provided == "" {
		if auth := r.Header.Get("Authorization"); auth != "" {
			// Strip an optional "Bearer " scheme; the scheme name is
			// case-insensitive per RFC 7235 §2.1.
			if len(auth) >= len(bearerPrefix) && strings.EqualFold(auth[:len(bearerPrefix)], bearerPrefix) {
				auth = auth[len(bearerPrefix):]
			}
			provided = strings.TrimSpace(auth)
		}
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(token)) == 1
}

// reject logs the denied request (never the token) and writes an HTTP error.
func reject(w http.ResponseWriter, r *http.Request, status int, reason string) {
	log.Warn().
		Str("remote_addr", r.RemoteAddr).
		Str("method", r.Method).
		Str("path", r.URL.Path).
		Str("host", r.Host).
		Str("origin", r.Header.Get("Origin")).
		Msgf("MCP SSE: rejected request: %s", reason)
	http.Error(w, reason, status)
}
