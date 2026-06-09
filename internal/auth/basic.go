package auth

import (
	"crypto/subtle"
	"net/http"
)

func BasicAuth(username, password string, next http.Handler) http.Handler {
	expectedUser := []byte(username)
	expectedPass := []byte(password)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok ||
			subtle.ConstantTimeCompare([]byte(u), expectedUser) != 1 ||
			subtle.ConstantTimeCompare([]byte(p), expectedPass) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="deploy-agent"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
