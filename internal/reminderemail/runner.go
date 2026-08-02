package reminderemail

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/bitofbytes-io/carma/internal/mailer"
	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/reminder"
	"github.com/bitofbytes-io/carma/internal/repository"
	"github.com/google/uuid"
)

const SuppressionWindow = 30 * 24 * time.Hour

var ErrRecipientCountMismatch = errors.New("reminder recipient count mismatch")

type Options struct {
	DryRun                bool
	ReminderID            *uuid.UUID
	RequireRecipientCount *int
}

type Report struct {
	LockContended, TargetFound bool
	Evaluated, Due             int
	Suppressed, Sent, Failed   int
	RecipientCount             int
}

type Runner struct {
	store     repository.ReminderEmailStore
	sender    mailer.Sender
	publicURL string
	logger    *slog.Logger
	now       func() time.Time
}

func NewRunner(store repository.ReminderEmailStore, sender mailer.Sender, publicURL string, logger *slog.Logger) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{store: store, sender: sender, publicURL: strings.TrimRight(publicURL, "/"), logger: logger, now: time.Now}
}

func (r *Runner) Run(ctx context.Context, options Options) (report Report, runErr error) {
	if !options.DryRun {
		unlock, acquired, err := r.store.TryReminderEmailLock(ctx)
		if err != nil {
			return report, err
		}
		if !acquired {
			report.LockContended = true
			r.logger.Info("reminder email run skipped", "reason", "lock_contended")
			return report, nil
		}
		defer func() {
			unlockContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			runErr = errors.Join(runErr, unlock(unlockContext))
		}()
	}

	reminders, err := r.store.ListReminders(ctx, nil, false)
	if err != nil {
		return report, fmt.Errorf("list reminders: %w", err)
	}
	if options.ReminderID != nil {
		filtered := reminders[:0]
		for _, candidate := range reminders {
			if candidate.ID == *options.ReminderID {
				filtered = append(filtered, candidate)
				report.TargetFound = true
			}
		}
		reminders = filtered
		if !report.TargetFound {
			return report, repository.ErrNotFound
		}
	} else {
		report.TargetFound = true
	}

	runTime := r.now().UTC()
	var sendErrors []error
	var recipients []string
	recipientsLoaded := false
	loadRecipients := func() error {
		if recipientsLoaded {
			return nil
		}
		rawRecipients, recipientErr := r.store.ListReminderRecipientEmails(ctx)
		if recipientErr != nil {
			return fmt.Errorf("list reminder recipients: %w", recipientErr)
		}
		recipients = NormalizeRecipients(rawRecipients)
		recipientsLoaded = true
		report.RecipientCount = len(recipients)
		return nil
	}
	for _, candidate := range reminders {
		if err := ctx.Err(); err != nil {
			return report, errors.Join(err, errors.Join(sendErrors...))
		}
		report.Evaluated++
		result := reminder.Evaluate(candidate, runTime)
		if result.Status == reminder.Due {
			report.Due++
		}
		if options.RequireRecipientCount != nil {
			if err := loadRecipients(); err != nil {
				return report, errors.Join(err, errors.Join(sendErrors...))
			}
			if len(recipients) != *options.RequireRecipientCount {
				return report, errors.Join(fmt.Errorf("%w: required %d, found %d", ErrRecipientCountMismatch, *options.RequireRecipientCount, len(recipients)), errors.Join(sendErrors...))
			}
		}
		if result.Status != reminder.Due {
			continue
		}
		suppressionCutoff := runTime.Add(-SuppressionWindow)
		if candidate.Baseline != nil && candidate.Baseline.CreatedAt.After(suppressionCutoff) {
			// A newly logged matching record starts a new maintenance cycle. Use
			// when Carma learned of that baseline, not its historical service date.
			suppressionCutoff = candidate.Baseline.CreatedAt.UTC()
		}
		suppressed, err := r.store.ReminderNotificationSince(ctx, candidate.ID, suppressionCutoff)
		if err != nil {
			report.Failed++
			sendErrors = append(sendErrors, fmt.Errorf("check reminder %s suppression: %w", candidate.ID, err))
			continue
		}
		if suppressed {
			report.Suppressed++
			continue
		}
		if !recipientsLoaded {
			if err := loadRecipients(); err != nil {
				return report, errors.Join(err, errors.Join(sendErrors...))
			}
		}
		if len(recipients) == 0 {
			return report, errors.Join(errors.New("no valid reminder email recipients"), errors.Join(sendErrors...))
		}
		if options.DryRun {
			continue
		}
		messageID, err := r.sender.Send(ctx, Render(result, r.publicURL, recipients))
		if err != nil {
			report.Failed++
			r.logger.Error("reminder email failed", "reminder_id", candidate.ID, "recipient_count", len(recipients), "error", err)
			sendErrors = append(sendErrors, fmt.Errorf("send reminder %s: %w", candidate.ID, err))
			continue
		}
		notification := model.ReminderNotification{
			ID: uuid.New(), ReminderID: candidate.ID, SentAt: r.now().UTC(),
			Recipients: append([]string(nil), recipients...), MessageID: messageID,
		}
		if err = r.store.CreateReminderNotification(ctx, notification); err != nil {
			report.Failed++
			r.logger.Error("reminder email audit failed", "reminder_id", candidate.ID, "message_id", messageID, "error", err)
			sendErrors = append(sendErrors, fmt.Errorf("audit reminder %s: %w", candidate.ID, err))
			continue
		}
		report.Sent++
		r.logger.Info("reminder email sent", "reminder_id", candidate.ID, "recipient_count", len(recipients), "message_id", messageID)
	}
	return report, errors.Join(sendErrors...)
}

func NormalizeRecipients(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	recipients := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		address, err := mail.ParseAddress(value)
		if err != nil || address.Name != "" || !strings.EqualFold(address.Address, value) {
			continue
		}
		local, domain, found := strings.Cut(address.Address, "@")
		if !found || local == "" || domain == "" || strings.ContainsAny(address.Address, "\r\n") {
			continue
		}
		normalized := strings.ToLower(address.Address)
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		recipients = append(recipients, normalized)
	}
	return recipients
}

func Render(result reminder.Result, publicURL string, recipients []string) mailer.Message {
	lines := []string{
		"A vehicle maintenance reminder is overdue.",
		"",
		"Vehicle: " + cleanBodyValue(result.Reminder.VehicleName),
		"Service: " + cleanBodyValue(result.Reminder.ServiceTypeName),
	}
	if result.DueDate != nil {
		lines = append(lines, "Due date: "+result.DueDate.UTC().Format("January 2, 2006"))
	}
	if result.DueMileage != nil {
		line := fmt.Sprintf("Due mileage: %d miles", *result.DueMileage)
		if result.Reminder.LatestOdometer != nil {
			line += fmt.Sprintf(" (latest: %d miles)", *result.Reminder.LatestOdometer)
		}
		lines = append(lines, line)
	}
	lines = append(lines, "", "View vehicle: "+strings.TrimRight(publicURL, "/")+"/vehicles/"+url.PathEscape(result.Reminder.VehicleID.String()), "")
	return mailer.Message{Recipients: append([]string(nil), recipients...), Subject: "Carma maintenance reminder", Body: strings.Join(lines, "\n")}
}

func cleanBodyValue(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
