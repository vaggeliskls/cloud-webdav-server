package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"golang.org/x/net/webdav"

	"cloud-webdav-server/internal/auth"
	"cloud-webdav-server/internal/config"
	"cloud-webdav-server/internal/permissions"
	"cloud-webdav-server/internal/storage"
)

// Server is the WebDAV HTTP server.
type Server struct {
	cfg     *config.Config
	httpSrv *http.Server
}

// New builds the server from the given config.
func New(cfg *config.Config) (*Server, error) {
	// --- Storage ---
	fs, err := storage.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("storage: %w", err)
	}

	// Auto-create folders if requested (all storage backends).
	if cfg.AutoCreateFolders {
		if err := ensureFolders(context.Background(), fs, cfg); err != nil {
			return nil, err
		}
	}

	// --- Permissions ---
	permMgr := permissions.New(cfg)

	// --- Auth ---
	authenticator := auth.New(cfg)
	slog.Info("auth", "provider", authenticator.Name())

	// --- WebDAV handler ---
	davHandler := &webdav.Handler{
		FileSystem: fs,
		LockSystem: webdav.NewMemLS(),
		Logger: func(r *http.Request, err error) {
			if err != nil {
				slog.Error("webdav", "method", r.Method, "path", r.URL.Path, "err", err)
			}
		},
	}

	// Wrap with directory listing handler.
	// golang.org/x/net/webdav returns 405 for GET on directories; this wrapper
	// intercepts those requests and renders an HTML listing instead.
	var rootHandler http.Handler = &dirListHandler{webdav: davHandler, fs: fs}

	// --- Middleware chain ---
	var middlewares []func(http.Handler) http.Handler
	middlewares = append(middlewares, loggingMiddleware)
	middlewares = append(middlewares, securityHeadersMiddleware)
	middlewares = append(middlewares, healthMiddleware)
	middlewares = append(middlewares, downloadMiddleware)
	if cfg.CORSEnabled {
		middlewares = append(middlewares, corsMiddleware(
			cfg.CORSOrigin,
			cfg.CORSAllowedMethods,
			cfg.CORSAllowedHeaders,
		))
	}
	if cfg.BrowserAccessBlocked {
		middlewares = append(middlewares, browserBlockMiddleware)
	}
	middlewares = append(middlewares, authPermMiddleware(authenticator, permMgr))

	handler := chain(rootHandler, middlewares...)

	addr := ":" + cfg.ServerPort
	httpSrv := &http.Server{
		Addr:         addr,
		Handler:      handler,
		ReadTimeout:  30 * time.Minute, // allow large file uploads
		WriteTimeout: 30 * time.Minute,
		IdleTimeout:  120 * time.Second,
	}

	return &Server{cfg: cfg, httpSrv: httpSrv}, nil
}

// Start begins listening and serving requests. It blocks until the context is cancelled.
func (s *Server) Start(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		slog.Info("webdav server starting",
			"addr", s.httpSrv.Addr,
			"storage", s.cfg.StorageType,
		)
		if err := s.httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		slog.Info("shutting down webdav server")
		return s.httpSrv.Shutdown(shutCtx)
	case err := <-errCh:
		return err
	}
}

// ensureFolders creates missing folders for all storage backends.
func ensureFolders(ctx context.Context, fs webdav.FileSystem, cfg *config.Config) error {
	for _, perm := range cfg.FolderPermissions {
		// Create ancestors first: Mkdir requires the parent to exist.
		var dir string
		for _, part := range strings.Split(strings.Trim(perm.Path, "/"), "/") {
			dir += "/" + part
			if _, err := fs.Stat(ctx, dir); err == nil {
				continue // already exists
			}
			if err := fs.Mkdir(ctx, dir, 0755); err != nil {
				return fmt.Errorf("auto-create folder %s: %w", dir, err)
			}
			slog.Info("folder ready", "path", dir)
		}
	}
	return nil
}
