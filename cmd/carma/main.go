package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/bitofbytes-io/carma/internal/assetcleanup"
	"github.com/bitofbytes-io/carma/internal/assets"
	"github.com/bitofbytes-io/carma/internal/auth"
	"github.com/bitofbytes-io/carma/internal/config"
	"github.com/bitofbytes-io/carma/internal/mailer"
	"github.com/bitofbytes-io/carma/internal/reminderemail"
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
	postgresStore, postgresBacked := store.(*repository.Postgres)
	var reminderRunner *reminderemail.Runner
	if cfg.ReminderEmail.Enabled {
		if !postgresBacked {
			return errors.New("reminder email requires postgres")
		}
		sender, err := mailer.NewSMTP(cfg.ReminderEmail.SMTPHost, cfg.ReminderEmail.SMTPUsername, cfg.ReminderEmail.SMTPPassword, cfg.ReminderEmail.FromAddress, cfg.ReminderEmail.FromName)
		if err != nil {
			return err
		}
		reminderRunner = reminderemail.NewRunner(postgresStore, sender, cfg.ReminderEmail.PublicURL, slog.Default())
	}
	httpServer := newHTTPServer(cfg.Port, app.Router())
	errs := make(chan error, 1)
	var scheduler sync.WaitGroup
	if postgresBacked {
		runner := assetcleanup.NewRunner(postgresStore, assetStore, slog.Default())
		scheduler.Add(1)
		go func() {
			defer scheduler.Done()
			assetcleanup.Schedule(ctx, assetcleanup.DefaultInterval, runner.Run)
		}()
	}
	if reminderRunner != nil {
		scheduler.Add(1)
		go func() {
			defer scheduler.Done()
			reminderemail.Schedule(ctx, reminderemail.DefaultInterval, reminderRunner.Run, slog.Default())
		}()
	}
	go func() {
		slog.Info("carma listening", "port", cfg.Port, "store", cfg.DataStore, "auth", cfg.AuthMode)
		errs <- httpServer.ListenAndServe()
	}()
	var result error
	select {
	case e := <-errs:
		if !errors.Is(e, http.ErrServerClosed) {
			result = e
		}
	case <-ctx.Done():
		shutdown, c := context.WithTimeout(context.Background(), 15*time.Second)
		defer c()
		result = httpServer.Shutdown(shutdown)
	}
	cancel()
	scheduler.Wait()
	return result
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
