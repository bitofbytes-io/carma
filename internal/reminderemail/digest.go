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
	"github.com/bitofbytes-io/carma/internal/nhtsa"
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
	// NewRecalls counts the recall campaigns announced for the first time.
	NewRecalls     int
	RecipientCount int
	Sent           bool
	// Message is the rendered digest, set whenever there was something to send.
	Message *mailer.Message
}

// DigestRunner emails one weekly summary of overdue and due-soon reminders
// and newly found NHTSA recalls across all vehicles. Recipients and time zone
// follow Runner.
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

// Run sends this week's digest unless it was already sent, nothing is overdue,
// due soon, or a new recall, or it is still before DigestSendHour on Monday. An
// empty week is not recorded, so a later run that week still sends once
// something comes due or a recall is found. Each recall is announced once:
// open, undismissed campaigns no earlier digest included.
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
		claimed, err := r.store.ReminderDigestClaimed(ctx, report.WeekStart)
		if err != nil {
			return report, fmt.Errorf("check reminder digest: %w", err)
		}
		if claimed {
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
	recalls, err := r.store.ListUnnotifiedRecalls(ctx)
	if err != nil {
		return report, fmt.Errorf("list new recalls: %w", err)
	}
	report.NewRecalls = len(recalls)
	if len(items) == 0 && len(recalls) == 0 {
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
	message := RenderDigest(items, recalls, report.WeekStart, r.publicURL, recipients)
	report.Message = &message
	if options.DryRun {
		return report, nil
	}
	// Claim the week before sending: once the relay may have the message, a
	// crash or failed write must never lead to a second send.
	digest := model.ReminderDigest{
		ID: uuid.New(), WeekStart: report.WeekStart, ReservedAt: r.now().UTC(),
		Recipients: append([]string(nil), recipients...),
	}
	for _, recall := range recalls {
		digest.Recalls = append(digest.Recalls, model.RecallKey{VehicleID: recall.VehicleID, CampaignNumber: recall.CampaignNumber})
	}
	reserved, err := r.store.ReserveReminderDigest(ctx, digest)
	if err != nil {
		return report, fmt.Errorf("reserve reminder digest: %w", err)
	}
	if !reserved {
		report.AlreadySent = true
		return report, nil
	}
	messageID, err := r.sender.Send(ctx, message)
	// Record the outcome even when ctx is canceled during shutdown. The timeout
	// starts only now, so a slow send cannot use up the write's time.
	recordContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), digestRecordTimeout)
	defer cancel()
	if err != nil {
		r.logger.Error("reminder digest failed", "week_start", report.WeekStart.Format(time.DateOnly), "recipient_count", len(recipients), "error", err)
		err = fmt.Errorf("send reminder digest: %w", err)
		if releaseErr := r.store.ReleaseReminderDigest(recordContext, digest.ID); releaseErr != nil {
			err = errors.Join(err, fmt.Errorf("release reminder digest: %w", releaseErr))
		}
		return report, err
	}
	report.Sent = true
	if err = r.store.CompleteReminderDigest(recordContext, digest.ID, messageID, r.now().UTC()); err != nil {
		// The claim still stands, so the week is not sent again.
		r.logger.Error("reminder digest audit failed", "week_start", report.WeekStart.Format(time.DateOnly), "message_id", messageID, "error", err)
		return report, fmt.Errorf("audit reminder digest: %w", err)
	}
	r.logger.Info("reminder digest sent", "week_start", report.WeekStart.Format(time.DateOnly), "overdue", report.Overdue, "due_soon", report.DueSoon, "new_recalls", report.NewRecalls, "recipient_count", len(recipients), "message_id", messageID)
	return report, nil
}

// digestRecordTimeout bounds the write that records or releases a claimed week
// after the send attempt.
const digestRecordTimeout = 5 * time.Second

// RenderDigest lists new recalls and then reminder items by vehicle in the
// order given, overdue before due soon within each vehicle.
func RenderDigest(items []reminder.Result, recalls []model.Recall, weekStart time.Time, publicURL string, recipients []string) mailer.Message {
	publicURL = strings.TrimRight(publicURL, "/")
	lines := []string{"Carma digest for the week of " + weekStart.Format("January 2, 2006") + ".", ""}
	if len(recalls) > 0 {
		lines = append(lines, recallLines(recalls, publicURL)...)
		if len(items) > 0 {
			lines = append(lines, "Maintenance", "")
		}
	}
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
	if len(items) > 0 {
		lines = append(lines, "Log a service record to reset a reminder. Manage reminders on each vehicle's Reminders page.")
	}
	var counts []string
	if len(recalls) == 1 {
		counts = append(counts, "1 new recall")
	} else if len(recalls) > 1 {
		counts = append(counts, fmt.Sprintf("%d new recalls", len(recalls)))
	}
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

// recallLines lists recalls by vehicle in the order given. NHTSA lists
// recalls per model year, so each links to NHTSA's lookup for the vehicle's
// VIN, when it is valid, and to the vehicle page where it can be dismissed.
func recallLines(recalls []model.Recall, publicURL string) []string {
	lines := []string{"New safety recalls", ""}
	for i, recall := range recalls {
		if i == 0 || recall.VehicleID != recalls[i-1].VehicleID {
			lines = append(lines, cleanBodyValue(recall.VehicleName))
		}
		component := cleanBodyValue(recall.Component)
		if component == "" {
			component = "Recall"
		}
		line := "  - " + component + " (campaign " + cleanBodyValue(recall.CampaignNumber)
		if recall.ReportReceivedOn != nil {
			line += ", reported " + recall.ReportReceivedOn.Format("Jan 2, 2006")
		}
		lines = append(lines, line+")")
		if recall.ParkIt {
			lines = append(lines, "    Do not drive this vehicle until the recall repair is done.")
		}
		if recall.ParkOutside {
			lines = append(lines, "    Park outside and away from buildings until the recall repair is done.")
		}
		if summary := truncate(cleanBodyValue(recall.Summary), recallSummaryLimit); summary != "" {
			lines = append(lines, "    "+summary)
		}
		if vin := nhtsa.LookupVIN(recall.VehicleVIN); vin != "" {
			lines = append(lines, "    Check this VIN: https://www.nhtsa.gov/recalls?vymm="+url.QueryEscape(vin))
		}
		lines = append(lines, "    Review or dismiss: "+publicURL+"/vehicles/"+url.PathEscape(recall.VehicleID.String())+"#recalls")
		if i == len(recalls)-1 || recall.VehicleID != recalls[i+1].VehicleID {
			lines = append(lines, "")
		}
	}
	return append(lines, "NHTSA lists recalls for a whole model year, so one may not apply to your VIN or may already be fixed. Dismiss it on the vehicle page once it is done or does not apply.", "")
}

// recallSummaryLimit caps how much of NHTSA's summary the digest quotes; the
// vehicle page has the rest.
const recallSummaryLimit = 300

// truncate shortens value to at most limit runes, cutting at a word boundary.
func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	cut := string(runes[:limit])
	if space := strings.LastIndex(cut, " "); space > 0 {
		cut = cut[:space]
	}
	return strings.TrimRight(cut, " ,;:.") + "..."
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
