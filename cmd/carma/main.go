package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bitofbytes-io/carma/internal/assets"
	"github.com/bitofbytes-io/carma/internal/auth"
	"github.com/bitofbytes-io/carma/internal/config"
	"github.com/bitofbytes-io/carma/internal/repository"
	"github.com/bitofbytes-io/carma/internal/server"
)

const (
	serverReadHeaderTimeout = 10 * time.Second
	// serverUploadTimeout allows the configured 128 MiB multipart request budget
	// to arrive over a slow mobile connection without leaving requests unbounded.
	serverUploadTimeout = 5 * time.Minute
	serverIdleTimeout   = 2 * time.Minute
)

func main() {
	if e := run(); e != nil {
		slog.Error("carma stopped", "error", e)
		os.Exit(1)
	}
}
func run() error {
	cfg, e := config.Load()
	if e != nil {
		return e
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var store repository.Store
	if cfg.DataStore == config.StoreMemory {
		store = repository.NewMemory()
	} else {
		store, e = repository.NewPostgres(ctx, cfg.DatabaseURL)
		if e != nil {
			return e
		}
	}
	defer store.Close()
	assetStore, e := assets.NewLocalStore(cfg.AssetRoot)
	if e != nil {
		return e
	}
	authService := auth.NewService(store, cfg.SessionTTL)
	var google auth.Google
	if cfg.AuthMode == config.AuthGoogle {
		google, e = auth.NewGoogleOIDC(ctx, cfg.GoogleClientID, cfg.GoogleClientSecret, cfg.GoogleRedirectURL, cfg.AllowedEmails, cfg.AllowedDomains)
		if e != nil {
			return e
		}
	}
	app, e := server.New(cfg, store, assetStore, authService, google)
	if e != nil {
		return e
	}
	httpServer := newHTTPServer(cfg.Port, app.Router())
	errs := make(chan error, 1)
	go func() {
		slog.Info("carma listening", "port", cfg.Port, "store", cfg.DataStore, "auth", cfg.AuthMode)
		errs <- httpServer.ListenAndServe()
	}()
	select {
	case e := <-errs:
		if !errors.Is(e, http.ErrServerClosed) {
			return e
		}
	case <-ctx.Done():
		shutdown, c := context.WithTimeout(context.Background(), 15*time.Second)
		defer c()
		return httpServer.Shutdown(shutdown)
	}
	return nil
}

func newHTTPServer(port string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
		ReadHeaderTimeout: serverReadHeaderTimeout,
		ReadTimeout:       serverUploadTimeout,
		WriteTimeout:      serverUploadTimeout,
		IdleTimeout:       serverIdleTimeout,
	}
}
