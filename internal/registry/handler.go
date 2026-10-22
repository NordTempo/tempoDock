package registry

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// Handler is the registry's token endpoint (Config.Realm), for the Docker
// token protocol (GET with basic auth, or the OAuth2 password POST).
//
// It never issues access of its own: a client must present a token the
// control plane minted, as the password (or as a bearer token), and gets
// that same token back if it is valid. Clients holding a token normally
// send it to the registry directly; this serves the ones that only follow
// the registry's challenge with credentials. It costs a signature check
// per call and keeps no state.
func (s *Signer) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		token := presented(r)
		if token == "" {
			w.Header().Set("WWW-Authenticate", `Basic realm="dock registry"`)
			http.Error(w, "a registry token minted by the control plane is required as the password", http.StatusUnauthorized)
			return
		}
		c, err := s.Verify(token)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		expiresIn := c.Expires - s.now().Unix()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":        token,
			"access_token": token,
			"expires_in":   expiresIn,
			"issued_at":    time.Unix(c.IssuedAt, 0).UTC().Format(time.RFC3339),
		})
	})
}

// presented returns the token a token request carries: the basic auth
// password, the OAuth2 password grant's password, or a bearer token.
func presented(r *http.Request) string {
	if _, pass, ok := r.BasicAuth(); ok {
		return pass
	}
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(nil, r.Body, 64<<10)
		if r.PostFormValue("grant_type") == "password" {
			return r.PostFormValue("password")
		}
	}
	if prefix, tok, ok := strings.Cut(r.Header.Get("Authorization"), " "); ok && strings.EqualFold(prefix, "bearer") {
		return tok
	}
	return ""
}
