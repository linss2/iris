package netutil

import (
	"net/http"
)

// used on host/supervisor/task and router/path

// IsTLS returns true if the "srv" contains any certificates
// or a get certificate function, meaning that is secure.
// 如果srv包含证书(srv.TLSConfig 这个字段)且cfg中的Certificates不为0且不为nil，则表示安全(这个方法则是安全认证)
func IsTLS(srv *http.Server) bool {
	if cfg := srv.TLSConfig; cfg != nil &&
		(len(cfg.Certificates) > 0 || cfg.GetCertificate != nil) {
		return true
	}

	return false
}

// ResolveSchemeFromServer tries to resolve a url scheme
// based on the server's configuration.
// Returns "https" on secure server,
// otherwise "http".
func ResolveSchemeFromServer(srv *http.Server) string {
	return ResolveScheme(IsTLS(srv))
}

// ResolveURLFromServer returns the scheme+host from a server.
func ResolveURLFromServer(srv *http.Server) string {
	scheme := ResolveSchemeFromServer(srv)
	host := ResolveVHost(srv.Addr)
	return scheme + "://" + host
}
