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
	"time"

	"github.com/bitofbytes-io/carma/internal/auth"
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
	if options.Digest {
		return runDigest(ctx, reminderemail.NewDigestRunner(store, sender, cfg.ReminderEmail.PublicURL, auth.AccessPolicy(cfg), cfg.Location, slog.Default()), options.DryRun, output)
	}
	runner := reminderemail.NewRunner(store, sender, cfg.ReminderEmail.PublicURL, auth.AccessPolicy(cfg), cfg.Location, slog.Default())
	report, err := runner.Run(ctx, options.Options)
	if _, printErr := fmt.Fprintf(output, "evaluated=%d due=%d recipients=%d suppressed=%d sent=%d failed=%d lock_contended=%t dry_run=%t\n", report.Evaluated, report.Due, report.RecipientCount, report.Suppressed, report.Sent, report.Failed, report.LockContended, options.DryRun); printErr != nil {
		return errors.Join(err, printErr)
	}
	return err
}

// runDigest runs the weekly digest once. A dry run prints the digest it would
// send instead of sending it.
func runDigest(ctx context.Context, runner *reminderemail.DigestRunner, dryRun bool, output io.Writer) error {
	report, err := runner.Run(ctx, reminderemail.DigestOptions{DryRun: dryRun})
	_, printErr := fmt.Fprintf(output, "week_start=%s overdue=%d due_soon=%d recipients=%d already_sent=%t too_early=%t sent=%t lock_contended=%t dry_run=%t\n", report.WeekStart.Format(time.DateOnly), report.Overdue, report.DueSoon, report.RecipientCount, report.AlreadySent, report.TooEarly, report.Sent, report.LockContended, dryRun)
	if dryRun && report.Message != nil && printErr == nil {
		_, printErr = fmt.Fprintf(output, "\nSubject: %s\n\n%s\n", report.Message.Subject, report.Message.Body)
	}
	return errors.Join(err, printErr)
}

type cliOptions struct {
	reminderemail.Options
	// Digest runs the weekly digest instead of overdue reminder emails.
	Digest bool
}

func parseOptions(args []string) (cliOptions, error) {
	var options cliOptions
	flags := flag.NewFlagSet("carma-reminders", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.BoolVar(&options.DryRun, "dry-run", false, "evaluate without locking, sending, or auditing")
	flags.BoolVar(&options.Digest, "digest", false, "send this week's due-soon digest instead of overdue reminders")
	var reminderID string
	flags.StringVar(&reminderID, "reminder-id", "", "process one reminder UUID")
	requiredRecipientCount := -1
	flags.IntVar(&requiredRecipientCount, "require-recipient-count", -1, "refuse a targeted run unless the normalized recipient count exactly matches")
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
	if requiredRecipientCount != -1 {
		if requiredRecipientCount < 1 {
			return options, errors.New("--require-recipient-count must be a positive integer")
		}
		if options.ReminderID == nil {
			return options, errors.New("--require-recipient-count requires --reminder-id")
		}
		options.RequireRecipientCount = &requiredRecipientCount
	}
	if options.Digest && options.ReminderID != nil {
		return options, errors.New("--digest cannot be combined with --reminder-id")
	}
	return options, nil
}
