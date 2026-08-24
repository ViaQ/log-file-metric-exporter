package auth

import (
	"net/http"
	"strings"

	log "github.com/ViaQ/logerr/v2/log/static"
)

// AuthMiddleware wraps an http.Handler with bearer token authentication and authorization.
// It extracts the token from the Authorization header, validates it via TokenReview,
// and checks authorization via SubjectAccessReview before delegating to the next handler.
func AuthMiddleware(authenticator *KubeAuthenticator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		token := strings.TrimPrefix(authHeader, "Bearer ")
		if token == "" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		status, err := authenticator.Authenticate(r.Context(), token)
		if err != nil {
			log.V(3).Info("authentication failed", "error", err)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		allowed, reason, err := authenticator.Authorize(r.Context(), status.User.Username, status.User.Groups, "get", r.URL.Path)
		if err != nil {
			log.V(3).Info("authorization check failed", "error", err)
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		if !allowed {
			log.V(3).Info("authorization denied", "user", status.User.Username, "reason", reason)
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}
