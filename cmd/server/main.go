package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"cloud-webdav-server/internal/config"
	"cloud-webdav-server/internal/server"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--healthcheck" {
		port := os.Getenv("SERVER_PORT")
		if port == "" {
			port = "8080"
		}
		// Fixed localhost target; only the port comes from config.
		resp, err := http.Get(fmt.Sprintf("http://localhost:%s/_health", port)) //nolint:gosec // G704: not user-controlled
		if err != nil || resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		os.Exit(0)
	}

	// Structured JSON logging in production; text for local dev.
	logLevel := slog.LevelInfo
	if os.Getenv("LOG_LEVEL") == "debug" {
		logLevel = slog.LevelDebug
	}
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel})
	slog.SetDefault(slog.New(handler))

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}

	srv, err := server.New(cfg)
	if err != nil {
		slog.Error("server init", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := srv.Start(ctx); err != nil {
		slog.Error("server", "err", err)
		os.Exit(1)
	}
	slog.Info("server stopped")
}
