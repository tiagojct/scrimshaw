package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/tiagojct/scrimshaw/internal/backup"
	"github.com/tiagojct/scrimshaw/internal/digest"
	"github.com/tiagojct/scrimshaw/internal/feeds"
	"github.com/tiagojct/scrimshaw/internal/fetch"
	"github.com/tiagojct/scrimshaw/internal/links"
	"github.com/tiagojct/scrimshaw/internal/reader"
	"github.com/tiagojct/scrimshaw/internal/server"
	"github.com/tiagojct/scrimshaw/internal/store"
)

func main() {
	// The scratch container image has no shell, so the binary doubles as its own
	// Docker HEALTHCHECK: `scrimshaw -healthcheck` exits 0 when /healthz is OK.
	if len(os.Args) > 1 && os.Args[1] == "-healthcheck" {
		os.Exit(runHealthcheck())
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	dataDir := env("SCRIMSHAW_DATA_DIR", "./data")
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		logger.Error("create data directory", "error", err)
		os.Exit(1)
	}
	secret, err := sessionSecret(dataDir)
	if err != nil {
		logger.Error("load session secret", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, err := store.Open(ctx, filepath.Join(dataDir, "scrimshaw.db"))
	if err != nil {
		logger.Error("open datastore", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	if fixed, err := feeds.BackfillBlankTitles(ctx, db); err != nil {
		logger.Error("backfill blank item titles", "error", err)
	} else if fixed > 0 {
		logger.Info("backfilled blank item titles", "count", fixed)
	}
	if err := db.OptimizeFTS(ctx); err != nil {
		logger.Warn("optimize FTS index", "error", err)
	}
	timeout, err := time.ParseDuration(env("SCRIMSHAW_FETCH_TIMEOUT", "30s"))
	if err != nil {
		logger.Error("invalid fetch timeout", "error", err)
		os.Exit(1)
	}
	saver := &reader.Saver{Store: db, Client: fetch.New(timeout), Snapshots: filepath.Join(dataDir, "snapshots")}
	feedService := &feeds.Service{Store: db, Client: fetch.New(timeout), Logger: logger, DisableAfter: 5, Snapshots: saver}
	go feedService.Run(ctx, time.Minute)
	linkChecker := &links.Checker{Store: db, Client: fetch.New(timeout), Logger: logger, Batch: 20, MaxAge: 24 * time.Hour}
	go linkChecker.Run(ctx, time.Hour)
	backupSvc := &backup.Service{Store: db, Dir: filepath.Join(dataDir, "backups"), Keep: 7, Logger: logger}
	go backupSvc.Run(ctx, 24*time.Hour)
	digestSvc := &digest.Service{Store: db, Directory: filepath.Join(dataDir, "exports", "digests"), Logger: logger}
	go digestSvc.Run(ctx, 7*24*time.Hour)
	app := server.New(db, logger, server.Config{SessionSecret: secret, CookieSecure: !strings.HasPrefix(env("SCRIMSHAW_BASE_URL", ""), "http://"), BaseURL: env("SCRIMSHAW_BASE_URL", ""), Saver: saver, Feeds: feedService, SnapshotsDir: filepath.Join(dataDir, "snapshots"), ImageCacheDir: filepath.Join(dataDir, "images"), Fetcher: fetch.New(timeout), ExportDir: filepath.Join(dataDir, "exports")})
	httpServer := &http.Server{Addr: env("SCRIMSHAW_ADDR", ":8080"), Handler: app.Routes(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	logger.Info("starting Scrimshaw", "address", httpServer.Addr)
	if err := httpServer.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// runHealthcheck requests /healthz on the local listen port and returns a
// process exit code (0 healthy, 1 not).
func runHealthcheck() int {
	host, port, err := net.SplitHostPort(env("SCRIMSHAW_ADDR", ":8080"))
	if err != nil {
		port = strings.TrimPrefix(env("SCRIMSHAW_ADDR", ":8080"), ":")
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		return 1
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return 0
	}
	return 1
}

func sessionSecret(dataDir string) ([]byte, error) {
	if configured := os.Getenv("SCRIMSHAW_SESSION_SECRET"); configured != "" {
		secret, err := base64.RawURLEncoding.DecodeString(configured)
		if err != nil || len(secret) < 32 {
			return nil, errors.New("SCRIMSHAW_SESSION_SECRET must be base64url encoded and at least 32 bytes")
		}
		return secret, nil
	}
	path := filepath.Join(dataDir, "session-secret")
	if secret, err := os.ReadFile(path); err == nil {
		return secret, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, secret, 0600); err != nil {
		return nil, err
	}
	return secret, nil
}
