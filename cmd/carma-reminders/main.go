package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/bitofbytes-io/carma/internal/config"
	"github.com/bitofbytes-io/carma/internal/mailer"
	"github.com/bitofbytes-io/carma/internal/reminderemail"
	"github.com/bitofbytes-io/carma/internal/repository"
	"github.com/google/uuid"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		slog.Error("carma reminders stopped", "error", err)
		os.Exit(1)
	}
}

func run(args []string, output io.Writer) error {
	options, err := parseOptions(args)
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if !cfg.ReminderEmail.Enabled {
		return errors.New("reminder email is disabled")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	store, err := repository.NewPostgres(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer store.Close()
	sender, err := mailer.NewSMTP(cfg.ReminderEmail.SMTPHost, cfg.ReminderEmail.SMTPUsername, cfg.ReminderEmail.SMTPPassword, cfg.ReminderEmail.FromAddress, cfg.ReminderEmail.FromName)
	if err != nil {
		return err
	}
	runner := reminderemail.NewRunner(store, sender, cfg.ReminderEmail.PublicURL, slog.Default())
	report, err := runner.Run(ctx, options)
	if _, printErr := fmt.Fprintf(output, "evaluated=%d due=%d suppressed=%d sent=%d failed=%d lock_contended=%t dry_run=%t\n", report.Evaluated, report.Due, report.Suppressed, report.Sent, report.Failed, report.LockContended, options.DryRun); printErr != nil {
		return errors.Join(err, printErr)
	}
	return err
}

func parseOptions(args []string) (reminderemail.Options, error) {
	var options reminderemail.Options
	flags := flag.NewFlagSet("carma-reminders", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.BoolVar(&options.DryRun, "dry-run", false, "evaluate without locking, sending, or auditing")
	var reminderID string
	flags.StringVar(&reminderID, "reminder-id", "", "process one reminder UUID")
	if err := flags.Parse(args); err != nil {
		return options, err
	}
	if flags.NArg() != 0 {
		return options, errors.New("unexpected positional arguments")
	}
	if reminderID != "" {
		id, err := uuid.Parse(reminderID)
		if err != nil {
			return options, fmt.Errorf("invalid --reminder-id: %w", err)
		}
		options.ReminderID = &id
	}
	return options, nil
}
