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
	"github.com/bitofbytes-io/carma/internal/schedule"
	"github.com/bitofbytes-io/carma/internal/server"
)

const (
	serverReadHeaderTimeout = 10 * time.Second
	// serverUploadTimeout allows the configured 128 MiB multipart request budget
	// to arrive over a slow mobile connection without leaving requests unbounded.
	serverUploadTimeout      = 5 * time.Minute
	serverIdleTimeout        = 2 * time.Minute
	schedulerShutdownTimeout = 15 * time.Second
)

var errSchedulerShutdownTimeout = errors.New("background scheduler shutdown timed out")

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
	store, e := repository.NewPostgres(ctx, cfg.DatabaseURL)
	if e != nil {
		return e
	}
	closeStoreOnReturn := true
	defer func() {
		if closeStoreOnReturn {
			store.Close()
		}
	}()
	assetStore, e := assets.NewLocalStore(cfg.AssetRoot)
	if e != nil {
		return e
	}
	allowed := auth.AccessPolicy(cfg)
	authService := auth.NewService(store, cfg.SessionTTL, allowed)
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
	var reminderRunner *reminderemail.Runner
	if cfg.ReminderEmail.Enabled {
		sender, err := mailer.NewSMTP(cfg.ReminderEmail.SMTPHost, cfg.ReminderEmail.SMTPUsername, cfg.ReminderEmail.SMTPPassword, cfg.ReminderEmail.FromAddress, cfg.ReminderEmail.FromName)
		if err != nil {
			return err
		}
		reminderRunner = reminderemail.NewRunner(store, sender, cfg.ReminderEmail.PublicURL, allowed, cfg.Location, slog.Default())
	}
	httpServer := newHTTPServer(cfg.Port, app.Router())
	errs := make(chan error, 1)
	var scheduler sync.WaitGroup
	every := func(interval time.Duration, job func(context.Context)) {
		scheduler.Add(1)
		go func() {
			defer scheduler.Done()
			schedule.Every(ctx, interval, job)
		}()
	}
	every(auth.SessionCleanupInterval, func(ctx context.Context) {
		if deleted, err := authService.DeleteExpiredSessions(ctx); err != nil {
			if ctx.Err() == nil {
				slog.Error("expired session cleanup failed", "error", err)
			}
		} else if deleted > 0 {
			slog.Info("expired sessions deleted", "count", deleted)
		}
	})
	assetCleanup := assetcleanup.NewRunner(store, assetStore, slog.Default())
	trigger := assetcleanup.TriggerStartup
	every(assetcleanup.DefaultInterval, func(ctx context.Context) {
		_, _ = assetCleanup.Run(ctx, trigger) // Run logs its own outcome.
		trigger = assetcleanup.TriggerScheduled
	})
	if reminderRunner != nil {
		every(reminderemail.DefaultInterval, func(ctx context.Context) {
			report, err := reminderRunner.Run(ctx, reminderemail.Options{})
			if err != nil && ctx.Err() == nil {
				slog.Error("reminder email run failed", "evaluated", report.Evaluated, "sent", report.Sent, "failed", report.Failed, "error", err)
			}
		})
	}
	go func() {
		slog.Info("carma listening", "port", cfg.Port, "auth", cfg.AuthMode)
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
	closeStoreOnReturn = false
	if e := closeStoreAfterSchedulers(&scheduler, schedulerShutdownTimeout, store.Close); e != nil {
		slog.Error("background scheduler shutdown timed out", "timeout", schedulerShutdownTimeout, "error", e)
		return errors.Join(result, e)
	}
	return result
}

func closeStoreAfterSchedulers(schedulers *sync.WaitGroup, timeout time.Duration, closeStore func()) error {
	done := make(chan struct{})
	go func() {
		schedulers.Wait()
		close(done)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		closeStore()
		return nil
	case <-timer.C:
		return errSchedulerShutdownTimeout
	}
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
