package reminderemail

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bitofbytes-io/carma/internal/mailer"
	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/reminder"
	"github.com/bitofbytes-io/carma/internal/repository"
	"github.com/google/uuid"
)

const (
	// DefaultDigestInterval is how often the server checks whether this week's
	// digest is due. Each week's digest is sent at most once.
	DefaultDigestInterval = time.Hour
	// DigestSendHour is the household-time hour on Monday from which the week's
	// digest may be sent, so it does not arrive overnight.
	DigestSendHour = 8
)

type DigestOptions struct {
	// DryRun evaluates and renders the digest without locking, sending, or
	// recording it.
	DryRun bool
}

type DigestReport struct {
	LockContended, AlreadySent, TooEarly bool
	WeekStart                            time.Time
	Overdue, DueSoon                     int
	RecipientCount                       int
	Sent                                 bool
	// Message is the rendered digest, set whenever there was something to send.
	Message *mailer.Message
}

// DigestRunner emails one weekly summary of overdue and due-soon reminders
// across all vehicles. Recipients and time zone follow Runner.
type DigestRunner struct {
	store     repository.ReminderDigestStore
	sender    mailer.Sender
	publicURL string
	allowed   func(string) bool
	location  *time.Location
	logger    *slog.Logger
	now       func() time.Time
}

func NewDigestRunner(store repository.ReminderDigestStore, sender mailer.Sender, publicURL string, allowed func(string) bool, location *time.Location, logger *slog.Logger) *DigestRunner {
	if logger == nil {
		logger = slog.Default()
	}
	if location == nil {
		location = time.UTC
	}
	return &DigestRunner{store: store, sender: sender, publicURL: strings.TrimRight(publicURL, "/"), allowed: allowed, location: location, logger: logger, now: time.Now}
}

// WeekStart returns the Monday of today's week (see reminder.Today).
func WeekStart(today time.Time) time.Time {
	return today.AddDate(0, 0, -((int(today.Weekday()) + 6) % 7))
}

// Run sends this week's digest unless it was already sent, nothing is overdue
// or due soon, or it is still before DigestSendHour on Monday. An empty week is
// not recorded, so a later run that week still sends once something comes due.
func (r *DigestRunner) Run(ctx context.Context, options DigestOptions) (report DigestReport, runErr error) {
	now := r.now()
	today := reminder.Today(now, r.location)
	report.WeekStart = WeekStart(today)
	if !options.DryRun {
		if now.Before(report.WeekStart.Add(DigestSendHour * time.Hour)) {
			report.TooEarly = true
			return report, nil
		}
		unlock, acquired, err := r.store.TryReminderDigestLock(ctx)
		if err != nil {
			return report, err
		}
		if !acquired {
			report.LockContended = true
			r.logger.Info("reminder digest run skipped", "reason", "lock_contended")
			return report, nil
		}
		defer func() {
			unlockContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			runErr = errors.Join(runErr, unlock(unlockContext))
		}()
		sent, err := r.store.ReminderDigestSent(ctx, report.WeekStart)
		if err != nil {
			return report, fmt.Errorf("check reminder digest: %w", err)
		}
		if sent {
			report.AlreadySent = true
			return report, nil
		}
	}

	reminders, err := r.store.ListReminders(ctx, nil, false)
	if err != nil {
		return report, fmt.Errorf("list reminders: %w", err)
	}
	points, err := r.store.ListMileagePoints(ctx, reminder.ProjectionVehicles(reminders), today.AddDate(0, -reminder.EstimateWindowMonths, 0))
	if err != nil {
		return report, fmt.Errorf("list mileage points: %w", err)
	}
	var items []reminder.Result
	for _, result := range reminder.EvaluateAll(reminders, points, today) {
		switch result.Status {
		case reminder.Due:
			report.Overdue++
		case reminder.Soon:
			report.DueSoon++
		default:
			continue
		}
		items = append(items, result)
	}
	if len(items) == 0 {
		return report, nil
	}

	rawRecipients, err := r.store.ListReminderRecipientEmails(ctx)
	if err != nil {
		return report, fmt.Errorf("list reminder recipients: %w", err)
	}
	recipients := NormalizeRecipients(rawRecipients)
	if r.allowed != nil {
		recipients = slices.DeleteFunc(recipients, func(email string) bool { return !r.allowed(email) })
	}
	report.RecipientCount = len(recipients)
	if len(recipients) == 0 {
		return report, errors.New("no valid reminder email recipients")
	}
	message := RenderDigest(items, report.WeekStart, r.publicURL, recipients)
	report.Message = &message
	if options.DryRun {
		return report, nil
	}
	messageID, err := r.sender.Send(ctx, message)
	if err != nil {
		r.logger.Error("reminder digest failed", "week_start", report.WeekStart.Format(time.DateOnly), "recipient_count", len(recipients), "error", err)
		return report, fmt.Errorf("send reminder digest: %w", err)
	}
	digest := model.ReminderDigest{
		ID: uuid.New(), WeekStart: report.WeekStart, SentAt: r.now().UTC(),
		Recipients: append([]string(nil), recipients...), MessageID: messageID,
	}
	if err = r.store.CreateReminderDigest(ctx, digest); err != nil {
		r.logger.Error("reminder digest audit failed", "week_start", report.WeekStart.Format(time.DateOnly), "message_id", messageID, "error", err)
		return report, fmt.Errorf("audit reminder digest: %w", err)
	}
	report.Sent = true
	r.logger.Info("reminder digest sent", "week_start", report.WeekStart.Format(time.DateOnly), "overdue", report.Overdue, "due_soon", report.DueSoon, "recipient_count", len(recipients), "message_id", messageID)
	return report, nil
}

// RenderDigest lists items by vehicle in the order given, overdue before due
// soon within each vehicle.
func RenderDigest(items []reminder.Result, weekStart time.Time, publicURL string, recipients []string) mailer.Message {
	publicURL = strings.TrimRight(publicURL, "/")
	var vehicles []uuid.UUID
	byVehicle := map[uuid.UUID][]reminder.Result{}
	overdue, soon := 0, 0
	for _, item := range items {
		id := item.Reminder.VehicleID
		if _, ok := byVehicle[id]; !ok {
			vehicles = append(vehicles, id)
		}
		byVehicle[id] = append(byVehicle[id], item)
		if item.Status == reminder.Due {
			overdue++
		} else {
			soon++
		}
	}
	lines := []string{"Vehicle maintenance for the week of " + weekStart.Format("January 2, 2006") + ".", ""}
	for _, id := range vehicles {
		vehicleItems := byVehicle[id]
		slices.SortStableFunc(vehicleItems, func(a, b reminder.Result) int {
			return statusRank(a.Status) - statusRank(b.Status)
		})
		lines = append(lines, cleanBodyValue(vehicleItems[0].Reminder.VehicleName))
		for _, item := range vehicleItems {
			lines = append(lines, digestLine(item))
		}
		lines = append(lines, "  "+publicURL+"/vehicles/"+url.PathEscape(id.String()), "")
	}
	lines = append(lines, "Log a service record to reset a reminder. Manage reminders on each vehicle's Reminders page.")
	var counts []string
	if overdue > 0 {
		counts = append(counts, fmt.Sprintf("%d overdue", overdue))
	}
	if soon > 0 {
		counts = append(counts, fmt.Sprintf("%d due soon", soon))
	}
	return mailer.Message{
		Recipients: append([]string(nil), recipients...),
		Subject:    "Carma weekly digest: " + strings.Join(counts, ", "),
		Body:       strings.Join(lines, "\n"),
	}
}

func statusRank(status reminder.Status) int {
	if status == reminder.Due {
		return 0
	}
	return 1
}

func digestLine(item reminder.Result) string {
	label := "Due soon"
	if item.Status == reminder.Due {
		label = "OVERDUE"
	}
	var details []string
	if item.DueDate != nil {
		details = append(details, "due "+item.DueDate.Format("Jan 2, 2006"))
	}
	if item.DueMileage != nil {
		details = append(details, "due at "+groupMiles(*item.DueMileage)+" mi")
	}
	if item.Detail != "" {
		// Miles left and the projected date, while the interval is not reached.
		details = append(details, item.Detail)
	} else if item.DueMileage != nil && item.Reminder.LatestOdometer != nil {
		details = append(details, "now "+groupMiles(*item.Reminder.LatestOdometer)+" mi")
	}
	line := "  - " + label + ": " + cleanBodyValue(item.Reminder.ServiceTypeName)
	if len(details) > 0 {
		line += " (" + strings.Join(details, "; ") + ")"
	}
	return line
}

// groupMiles formats a nonnegative mileage with comma separators.
func groupMiles(n int64) string {
	digits := strconv.FormatInt(n, 10)
	for i := len(digits) - 3; i > 0; i -= 3 {
		digits = digits[:i] + "," + digits[i:]
	}
	return digits
}
