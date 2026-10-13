package server

import (
	"context"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"cloud-webdav-server/internal/auth"
	"cloud-webdav-server/internal/permissions"
)

// contextKey is a typed key for context values.
type contextKey string

const usernameKey contextKey = "username"

// UsernameFromContext retrieves the authenticated username from context.
func UsernameFromContext(ctx context.Context) string {
	v, _ := ctx.Value(usernameKey).(string)
	return v
}

// loggingMiddleware logs each request with method, path, status, and duration.
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		if r.URL.Path == "/_health" && rw.status == http.StatusOK {
			return
		}
		if r.URL.Path == "/favicon.ico" {
			return
		}
		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.status,
			"duration", time.Since(start).String(),
			"remote", r.RemoteAddr,
			"user", UsernameFromContext(r.Context()),
		)
	})
}

// corsMiddleware adds CORS headers when enabled.
func corsMiddleware(origin, methods, headers string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", methods)
			w.Header().Set("Access-Control-Allow-Headers", headers)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// healthMiddleware responds to /_health with 200 OK and /favicon.ico with 404.
func healthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_health" {
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "OK")
			return
		}
		if r.URL.Path == "/favicon.ico" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// browserBlockMiddleware returns 403 for requests from web browsers.
func browserBlockMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua := r.Header.Get("User-Agent")
		if strings.Contains(ua, "Mozilla") {
			renderError(w, r, http.StatusForbidden, "Browser access is blocked for this path.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// downloadMiddleware forces file responses to be downloaded (Content-Disposition: attachment).
// Directory listings (text/html) are not affected.
func downloadMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(&downloadResponseWriter{ResponseWriter: w, path: r.URL.Path}, r)
	})
}

type downloadResponseWriter struct {
	http.ResponseWriter
	path        string
	wroteHeader bool
}

func (w *downloadResponseWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.wroteHeader = true
		ct := w.ResponseWriter.Header().Get("Content-Type")
		if !strings.HasPrefix(ct, "text/html") {
			filename := strings.TrimSuffix(w.path[strings.LastIndex(w.path, "/")+1:], "/")
			if filename != "" {
				// FormatMediaType quotes/escapes the name and uses RFC 2231
				// encoding for non-ASCII characters.
				w.ResponseWriter.Header().Set("Content-Disposition",
					mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
			}
		}
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *downloadResponseWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

// securityHeadersMiddleware adds basic security headers.
func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// authPermMiddleware authenticates the request and checks folder permissions.
// It sets the username in the request context on success.
func authPermMiddleware(
	authenticator auth.Authenticator,
	permMgr *permissions.Manager,
) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Always allow OPTIONS preflight without auth.
			if r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}

			username, _ := authenticator.Authenticate(r.Context(), r)

			result := permMgr.Check(r.URL.Path, r.Method, username)
			switch result {
			case permissions.Allow:
				ctx := context.WithValue(r.Context(), usernameKey, username)
				next.ServeHTTP(w, r.WithContext(ctx))
			case permissions.DenyUnauth:
				realm := permMgr.Realm(r.URL.Path)
				w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Basic realm=%q`, realm))
				renderError(w, r, http.StatusUnauthorized, "Authentication is required to access this resource.")
			case permissions.DenyForbid:
				renderError(w, r, http.StatusForbidden, "You don't have permission to access this resource.")
			}
		})
	}
}

// chain applies middlewares right-to-left (outermost first).
func chain(h http.Handler, middlewares ...func(http.Handler) http.Handler) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		h = middlewares[i](h)
	}
	return h
}

// responseWriter captures the status code for logging.
type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}
