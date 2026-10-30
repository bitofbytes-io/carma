package reminderemail

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/mailer"
	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/google/uuid"
)

type fakeDigest struct {
	model.ReminderDigest
	MessageID string
}

type fakeDigestStore struct {
	reminders []model.Reminder
	points    []model.MileagePoint
	emails    []string
	recalls   []model.Recall
	// notified maps each announced recall to the digest that claimed it.
	notified      map[model.RecallKey]uuid.UUID
	digests       []fakeDigest
	lockAvailable bool
	lockCalls     int
	unlockCalls   int
	completeErr   error
	// releaseDeadline is the deadline of the last release call's context.
	releaseDeadline time.Time
	// claimOnCheck makes the week look unclaimed to the first check and
	// claimed on reservation, as if another writer raced it.
	claimOnCheck bool
}

func (f *fakeDigestStore) ListReminders(context.Context, *uuid.UUID, bool) ([]model.Reminder, error) {
	return append([]model.Reminder(nil), f.reminders...), nil
}
func (f *fakeDigestStore) ListMileagePoints(context.Context, []uuid.UUID, time.Time) ([]model.MileagePoint, error) {
	return append([]model.MileagePoint(nil), f.points...), nil
}
func (f *fakeDigestStore) ListReminderRecipientEmails(context.Context) ([]string, error) {
	return append([]string(nil), f.emails...), nil
}
func (f *fakeDigestStore) ListUnnotifiedRecalls(context.Context) ([]model.Recall, error) {
	var recalls []model.Recall
	for _, recall := range f.recalls {
		if _, ok := f.notified[model.RecallKey{VehicleID: recall.VehicleID, CampaignNumber: recall.CampaignNumber}]; !ok {
			recalls = append(recalls, recall)
		}
	}
	return recalls, nil
}
func (f *fakeDigestStore) ReminderDigestClaimed(_ context.Context, weekStart time.Time) (bool, error) {
	for _, digest := range f.digests {
		if digest.WeekStart.Equal(weekStart) {
			return true, nil
		}
	}
	return false, nil
}
func (f *fakeDigestStore) ReserveReminderDigest(ctx context.Context, digest model.ReminderDigest) (bool, error) {
	if f.claimOnCheck {
		return false, nil
	}
	if claimed, _ := f.ReminderDigestClaimed(ctx, digest.WeekStart); claimed {
		return false, nil
	}
	f.digests = append(f.digests, fakeDigest{ReminderDigest: digest})
	for _, key := range digest.Recalls {
		if f.notified == nil {
			f.notified = map[model.RecallKey]uuid.UUID{}
		}
		if _, ok := f.notified[key]; !ok {
			f.notified[key] = digest.ID
		}
	}
	return true, nil
}
func (f *fakeDigestStore) CompleteReminderDigest(ctx context.Context, id uuid.UUID, messageID string, _ time.Time) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if f.completeErr != nil {
		return f.completeErr
	}
	for i := range f.digests {
		if f.digests[i].ID == id {
			f.digests[i].MessageID = messageID
			return nil
		}
	}
	return errors.New("no reservation")
}
func (f *fakeDigestStore) ReleaseReminderDigest(ctx context.Context, id uuid.UUID) error {
	f.releaseDeadline, _ = ctx.Deadline()
	before := len(f.digests)
	f.digests = slices.DeleteFunc(f.digests, func(digest fakeDigest) bool { return digest.ID == id && digest.MessageID == "" })
	if len(f.digests) < before {
		maps.DeleteFunc(f.notified, func(_ model.RecallKey, digestID uuid.UUID) bool { return digestID == id })
	}
	return nil
}
func (f *fakeDigestStore) TryReminderDigestLock(context.Context) (func(context.Context) error, bool, error) {
	f.lockCalls++
	if !f.lockAvailable {
		return nil, false, nil
	}
	return func(context.Context) error { f.unlockCalls++; return nil }, true, nil
}

func newTestDigestRunner(store *fakeDigestStore, sender mailer.Sender, now time.Time) *DigestRunner {
	runner := NewDigestRunner(store, sender, "https://carma.bitofbytes.io/", nil, time.UTC, slog.New(slog.NewTextHandler(io.Discard, nil)))
	runner.now = func() time.Time { return now }
	return runner
}

// digestFixture returns reminders on Wednesday, March 18, 2026: an overdue and
// a due-soon reminder on the Roadster, a mileage reminder on the Truck that is
// due soon only by projection, and one that is fine.
func digestFixture(now time.Time) (*fakeDigestStore, uuid.UUID, uuid.UUID) {
	roadster, truck := uuid.New(), uuid.New()
	month, year := 1, 12
	interval, start, latest := int64(5000), int64(1000), int64(5000)
	store := &fakeDigestStore{lockAvailable: true, emails: []string{"user@example.com"}, reminders: []model.Reminder{
		{ID: uuid.New(), VehicleID: roadster, VehicleName: "Roadster", ServiceTypeName: "Wipers", IntervalMonths: &month, Enabled: true, CreatedAt: now.AddDate(0, 0, -20)},
		{ID: uuid.New(), VehicleID: roadster, VehicleName: "Roadster", ServiceTypeName: "Oil change", IntervalMonths: &month, Enabled: true, CreatedAt: now.AddDate(0, -2, 0)},
		{ID: uuid.New(), VehicleID: roadster, VehicleName: "Roadster", ServiceTypeName: "Registration", IntervalMonths: &year, Enabled: true, CreatedAt: now.AddDate(0, -1, 0)},
		{ID: uuid.New(), VehicleID: truck, VehicleName: "Truck", ServiceTypeName: "Tire rotation", IntervalMiles: &interval, StartingOdometer: &start, LatestOdometer: &latest, Enabled: true, CreatedAt: now.AddDate(0, -3, 0)},
	}, points: []model.MileagePoint{
		{VehicleID: truck, On: now.AddDate(0, 0, -30), Miles: 4000},
		{VehicleID: truck, On: now, Miles: 5000},
	}}
	return store, roadster, truck
}

func TestDigestSendsOncePerWeekGroupedByVehicle(t *testing.T) {
	now := time.Date(2026, time.March, 18, 12, 0, 0, 0, time.UTC)
	store, roadster, truck := digestFixture(now)
	sender := &fakeSender{}
	runner := newTestDigestRunner(store, sender, now)

	report, err := runner.Run(context.Background(), DigestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	monday := time.Date(2026, time.March, 16, 0, 0, 0, 0, time.UTC)
	if !report.Sent || report.Overdue != 1 || report.DueSoon != 2 || !report.WeekStart.Equal(monday) || report.RecipientCount != 1 {
		t.Fatalf("report = %+v", report)
	}
	if len(sender.messages) != 1 || len(store.digests) != 1 || !store.digests[0].WeekStart.Equal(monday) || store.digests[0].MessageID == "" {
		t.Fatalf("messages=%d digests=%+v", len(sender.messages), store.digests)
	}
	message := sender.messages[0]
	if message.Subject != "Carma weekly digest: 1 overdue, 2 due soon" || !slices.Equal(message.Recipients, []string{"user@example.com"}) {
		t.Fatalf("message = %+v", message)
	}
	body := message.Body
	for _, want := range []string{
		"week of March 16, 2026",
		"  - OVERDUE: Oil change (due Feb 18, 2026)",
		"  - Due soon: Wipers (due Mar 26, 2026)",
		"  - Due soon: Tire rotation (due at 6,000 mi; ~1,000 mi left, est. Apr 17)",
		"https://carma.bitofbytes.io/vehicles/" + roadster.String(),
		"https://carma.bitofbytes.io/vehicles/" + truck.String(),
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("digest missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Registration") {
		t.Fatalf("digest lists a reminder that is not due:\n%s", body)
	}
	if strings.Index(body, "OVERDUE: Oil change") > strings.Index(body, "Due soon: Wipers") || strings.Index(body, "Roadster") > strings.Index(body, "Truck") {
		t.Fatalf("digest order:\n%s", body)
	}
	if store.lockCalls != 1 || store.unlockCalls != 1 {
		t.Fatalf("lock calls=%d unlock=%d", store.lockCalls, store.unlockCalls)
	}

	runner.now = func() time.Time { return now.AddDate(0, 0, 4) }
	report, err = runner.Run(context.Background(), DigestOptions{})
	if err != nil || !report.AlreadySent || report.Sent || len(sender.messages) != 1 {
		t.Fatalf("second run in the same week: report=%+v err=%v messages=%d", report, err, len(sender.messages))
	}

	runner.now = func() time.Time { return now.AddDate(0, 0, 5) }
	report, err = runner.Run(context.Background(), DigestOptions{})
	if err != nil || !report.Sent || len(sender.messages) != 2 || !report.WeekStart.Equal(monday.AddDate(0, 0, 7)) {
		t.Fatalf("next week: report=%+v err=%v messages=%d", report, err, len(sender.messages))
	}
}

func TestDigestWaitsForMondayMorningInHouseholdTimeZone(t *testing.T) {
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	// 11:30 UTC is 07:30 in New York on Monday, March 16, 2026.
	now := time.Date(2026, time.March, 16, 11, 30, 0, 0, time.UTC)
	store, _, _ := digestFixture(now)
	sender := &fakeSender{}
	runner := newTestDigestRunner(store, sender, now)
	runner.location = newYork
	report, err := runner.Run(context.Background(), DigestOptions{})
	if err != nil || !report.TooEarly || report.Sent || store.lockCalls != 0 || len(sender.messages) != 0 {
		t.Fatalf("report=%+v err=%v lock=%d", report, err, store.lockCalls)
	}
	runner.now = func() time.Time { return now.Add(time.Hour) }
	if report, err = runner.Run(context.Background(), DigestOptions{}); err != nil || !report.Sent {
		t.Fatalf("08:30 run: report=%+v err=%v", report, err)
	}
	if want := time.Date(2026, time.March, 16, 0, 0, 0, 0, newYork); !report.WeekStart.Equal(want) {
		t.Fatalf("week start = %v, want %v", report.WeekStart, want)
	}
}

func TestDigestSkipsSnoozedReminders(t *testing.T) {
	now := time.Date(2026, time.March, 18, 12, 0, 0, 0, time.UTC)
	store, _, _ := digestFixture(now)
	later := now.AddDate(0, 0, 14)
	miles := int64(6000)
	for i := range store.reminders {
		switch store.reminders[i].ServiceTypeName {
		case "Oil change":
			store.reminders[i].SnoozedUntilOn = &later
		case "Tire rotation":
			store.reminders[i].SnoozedUntilMiles = &miles
		}
	}
	sender := &fakeSender{}
	report, err := newTestDigestRunner(store, sender, now).Run(context.Background(), DigestOptions{})
	if err != nil || !report.Sent || report.Overdue != 0 || report.DueSoon != 1 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	body := sender.messages[0].Body
	if sender.messages[0].Subject != "Carma weekly digest: 1 due soon" || !strings.Contains(body, "Due soon: Wipers") || strings.Contains(body, "Oil change") || strings.Contains(body, "Tire rotation") {
		t.Fatalf("snoozed reminders leaked into the digest:\n%s", body)
	}

	store, _, _ = digestFixture(now)
	for i := range store.reminders {
		switch store.reminders[i].ServiceTypeName {
		case "Oil change", "Wipers":
			store.reminders[i].SnoozedUntilOn = &later
		case "Tire rotation":
			store.reminders[i].SnoozedUntilMiles = &miles
		}
	}
	sender = &fakeSender{}
	report, err = newTestDigestRunner(store, sender, now).Run(context.Background(), DigestOptions{})
	if err != nil || report.Sent || report.Overdue != 0 || report.DueSoon != 0 || report.Message != nil || len(sender.messages) != 0 || len(store.digests) != 0 {
		t.Fatalf("fully snoozed week: report=%+v err=%v messages=%d", report, err, len(sender.messages))
	}
}

func TestDigestIncludesReminderWhoseSnoozeHasPassed(t *testing.T) {
	now := time.Date(2026, time.March, 18, 12, 0, 0, 0, time.UTC)
	store, _, _ := digestFixture(now)
	today := time.Date(2026, time.March, 18, 0, 0, 0, 0, time.UTC)
	for i := range store.reminders {
		if store.reminders[i].ServiceTypeName == "Oil change" {
			store.reminders[i].SnoozedUntilOn = &today
		}
	}
	sender := &fakeSender{}
	report, err := newTestDigestRunner(store, sender, now).Run(context.Background(), DigestOptions{})
	if err != nil || !report.Sent || report.Overdue != 1 || report.DueSoon != 2 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if !strings.Contains(sender.messages[0].Body, "OVERDUE: Oil change") {
		t.Fatalf("passed snooze was still hidden:\n%s", sender.messages[0].Body)
	}
}

func TestDigestSkipsEmptyWeekWithoutRecordingIt(t *testing.T) {
	now := time.Date(2026, time.March, 18, 12, 0, 0, 0, time.UTC)
	month := 6
	store := &fakeDigestStore{lockAvailable: true, emails: []string{"user@example.com"}, reminders: []model.Reminder{
		{ID: uuid.New(), VehicleID: uuid.New(), IntervalMonths: &month, Enabled: true, CreatedAt: now},
	}}
	sender := &fakeSender{}
	report, err := newTestDigestRunner(store, sender, now).Run(context.Background(), DigestOptions{})
	if err != nil || report.Sent || report.Message != nil || len(sender.messages) != 0 || len(store.digests) != 0 || store.unlockCalls != 1 {
		t.Fatalf("report=%+v err=%v digests=%+v", report, err, store.digests)
	}
}

func TestDigestSendFailureIsRetried(t *testing.T) {
	now := time.Date(2026, time.March, 18, 12, 0, 0, 0, time.UTC)
	store, _, _ := digestFixture(now)
	sender := &fakeSender{err: errors.New("smtp down")}
	runner := newTestDigestRunner(store, sender, now)
	if report, err := runner.Run(context.Background(), DigestOptions{}); err == nil || report.Sent || len(store.digests) != 0 || store.unlockCalls != 1 {
		t.Fatalf("report=%+v err=%v digests=%+v", report, err, store.digests)
	}
	sender.err = nil
	if report, err := runner.Run(context.Background(), DigestOptions{}); err != nil || !report.Sent || len(store.digests) != 1 {
		t.Fatalf("retry: report=%+v err=%v", report, err)
	}
}

func TestDigestReleaseGetsFullTimeoutAfterSlowSend(t *testing.T) {
	now := time.Date(2026, time.March, 18, 12, 0, 0, 0, time.UTC)
	store, _, _ := digestFixture(now)
	sender := &slowFailingSender{delay: 20 * time.Millisecond}
	if _, err := newTestDigestRunner(store, sender, now).Run(context.Background(), DigestOptions{}); err == nil {
		t.Fatal("expected send error")
	}
	if len(store.digests) != 0 {
		t.Fatalf("claim was not released: %+v", store.digests)
	}
	if remaining := store.releaseDeadline.Sub(sender.returned); remaining < digestRecordTimeout {
		t.Fatalf("release had %v after the send returned, want %v", remaining, digestRecordTimeout)
	}
}

// slowFailingSender fails after a delay, recording when it returned.
type slowFailingSender struct {
	delay    time.Duration
	returned time.Time
}

func (s *slowFailingSender) Send(context.Context, mailer.Message) (string, error) {
	time.Sleep(s.delay)
	s.returned = time.Now()
	return "", errors.New("smtp timeout")
}

func TestDigestAuditFailureAfterDeliveryDoesNotResend(t *testing.T) {
	now := time.Date(2026, time.March, 18, 12, 0, 0, 0, time.UTC)
	store, _, _ := digestFixture(now)
	store.completeErr = errors.New("database unavailable")
	sender := &fakeSender{}
	runner := newTestDigestRunner(store, sender, now)
	if report, err := runner.Run(context.Background(), DigestOptions{}); err == nil || !report.Sent || len(sender.messages) != 1 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	store.completeErr = nil
	runner.now = func() time.Time { return now.Add(time.Hour) }
	if report, err := runner.Run(context.Background(), DigestOptions{}); err != nil || !report.AlreadySent || len(sender.messages) != 1 {
		t.Fatalf("retry after audit failure: report=%+v err=%v messages=%d", report, err, len(sender.messages))
	}
}

func TestDigestRecordsDeliveryWhenCanceledDuringSend(t *testing.T) {
	now := time.Date(2026, time.March, 18, 12, 0, 0, 0, time.UTC)
	store, _, _ := digestFixture(now)
	ctx, cancel := context.WithCancel(context.Background())
	sender := &cancelingSender{cancel: cancel}
	runner := NewDigestRunner(store, sender, "https://carma.bitofbytes.io", nil, time.UTC, slog.New(slog.NewTextHandler(io.Discard, nil)))
	runner.now = func() time.Time { return now }
	if report, err := runner.Run(ctx, DigestOptions{}); err != nil || !report.Sent {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if len(store.digests) != 1 || store.digests[0].MessageID != "<canceled@bitofbytes.io>" {
		t.Fatalf("digests=%+v", store.digests)
	}
}

// cancelingSender accepts the message and then cancels the run's context, as a
// shutdown arriving right after delivery would.
type cancelingSender struct{ cancel context.CancelFunc }

func (s *cancelingSender) Send(context.Context, mailer.Message) (string, error) {
	s.cancel()
	return "<canceled@bitofbytes.io>", nil
}

func TestDigestConcurrentClaimSkipsSend(t *testing.T) {
	now := time.Date(2026, time.March, 18, 12, 0, 0, 0, time.UTC)
	store, _, _ := digestFixture(now)
	store.claimOnCheck = true
	sender := &fakeSender{}
	report, err := newTestDigestRunner(store, sender, now).Run(context.Background(), DigestOptions{})
	if err != nil || !report.AlreadySent || report.Sent || len(sender.messages) != 0 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestDigestDryRunAndLockContention(t *testing.T) {
	now := time.Date(2026, time.March, 16, 1, 0, 0, 0, time.UTC)
	store, _, _ := digestFixture(now)
	store.lockAvailable = false
	sender := &fakeSender{}
	runner := newTestDigestRunner(store, sender, now)
	report, err := runner.Run(context.Background(), DigestOptions{DryRun: true})
	if err != nil || report.Message == nil || report.Sent || store.lockCalls != 0 || len(sender.messages) != 0 || len(store.digests) != 0 {
		t.Fatalf("dry run: report=%+v err=%v", report, err)
	}
	runner.now = func() time.Time { return now.Add(12 * time.Hour) }
	report, err = runner.Run(context.Background(), DigestOptions{})
	if err != nil || !report.LockContended || report.Sent || len(sender.messages) != 0 {
		t.Fatalf("contended: report=%+v err=%v", report, err)
	}
}

func TestWeekStartIsMonday(t *testing.T) {
	for day := 16; day <= 22; day++ {
		got := WeekStart(time.Date(2026, time.March, day, 0, 0, 0, 0, time.UTC))
		if want := time.Date(2026, time.March, 16, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
			t.Fatalf("WeekStart(March %d) = %v, want %v", day, got, want)
		}
	}
}

// recallFixture returns new recalls on two vehicles: two on the Roadster,
// whose VIN is valid, and one on the Truck, whose VIN is not.
func recallFixture(roadster, truck uuid.UUID) []model.Recall {
	received := time.Date(2026, time.March, 5, 0, 0, 0, 0, time.UTC)
	return []model.Recall{
		{VehicleID: roadster, VehicleName: "Roadster", VehicleVIN: "1hgcm-82633a004352", CampaignNumber: "26V101000", Component: "FUEL SYSTEM, GASOLINE", Summary: "The fuel pump may fail.", ReportReceivedOn: &received, ParkIt: true},
		{VehicleID: roadster, VehicleName: "Roadster", VehicleVIN: "1hgcm-82633a004352", CampaignNumber: "26V102000", Summary: strings.Repeat("word ", 100)},
		{VehicleID: truck, VehicleName: "Truck", VehicleVIN: "UNKNOWN", CampaignNumber: "26V103000", Component: "AIR BAGS", ParkOutside: true},
	}
}

func TestDigestAnnouncesEachNewRecallOnce(t *testing.T) {
	now := time.Date(2026, time.March, 18, 12, 0, 0, 0, time.UTC)
	roadster, truck := uuid.New(), uuid.New()
	store := &fakeDigestStore{lockAvailable: true, emails: []string{"user@example.com"}, recalls: recallFixture(roadster, truck)}
	sender := &fakeSender{}
	runner := newTestDigestRunner(store, sender, now)

	report, err := runner.Run(context.Background(), DigestOptions{})
	if err != nil || !report.Sent || report.NewRecalls != 3 || report.Overdue != 0 || report.DueSoon != 0 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if len(sender.messages) != 1 || len(store.digests) != 1 || len(store.digests[0].Recalls) != 3 || len(store.notified) != 3 {
		t.Fatalf("messages=%d digests=%+v notified=%v", len(sender.messages), store.digests, store.notified)
	}
	message := sender.messages[0]
	if message.Subject != "Carma weekly digest: 3 new recalls" {
		t.Fatalf("subject = %q", message.Subject)
	}
	body := message.Body
	roadsterPage := "https://carma.bitofbytes.io/vehicles/" + roadster.String() + "#recalls"
	for _, want := range []string{
		"New safety recalls\n\nRoadster\n  - FUEL SYSTEM, GASOLINE (campaign 26V101000, reported Mar 5, 2026)\n",
		"    Do not drive this vehicle until the recall repair is done.\n    The fuel pump may fail.\n",
		"    Check this VIN: https://www.nhtsa.gov/recalls?vymm=1HGCM82633A004352\n    Review or dismiss: " + roadsterPage + "\n",
		"  - Recall (campaign 26V102000)\n",
		"Truck\n  - AIR BAGS (campaign 26V103000)\n    Park outside and away from buildings until the recall repair is done.\n    Review or dismiss: https://carma.bitofbytes.io/vehicles/" + truck.String() + "#recalls\n",
		"may not apply to your VIN",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("digest missing %q:\n%s", want, body)
		}
	}
	if strings.Count(body, "vymm=") != 2 || strings.Contains(body, "Maintenance") || strings.Contains(body, "Log a service record") {
		t.Fatalf("digest should link only the valid VIN and have no maintenance section:\n%s", body)
	}
	if strings.Contains(body, strings.Repeat("word ", 70)) || !strings.Contains(body, "word...") {
		t.Fatalf("long summary not truncated:\n%s", body)
	}

	// Announced recalls alone do not send next week's digest.
	runner.now = func() time.Time { return now.AddDate(0, 0, 7) }
	report, err = runner.Run(context.Background(), DigestOptions{})
	if err != nil || report.Sent || report.NewRecalls != 0 || len(sender.messages) != 1 {
		t.Fatalf("next week: report=%+v err=%v messages=%d", report, err, len(sender.messages))
	}
	// A recall found later is announced in the following digest on its own.
	store.recalls = append(store.recalls, model.Recall{VehicleID: truck, VehicleName: "Truck", CampaignNumber: "26V200000", Component: "BRAKES"})
	report, err = runner.Run(context.Background(), DigestOptions{})
	if err != nil || !report.Sent || report.NewRecalls != 1 || len(sender.messages) != 2 {
		t.Fatalf("new recall: report=%+v err=%v messages=%d", report, err, len(sender.messages))
	}
	if body := sender.messages[1].Body; sender.messages[1].Subject != "Carma weekly digest: 1 new recall" || !strings.Contains(body, "BRAKES") || strings.Contains(body, "AIR BAGS") {
		t.Fatalf("second digest = %+v", sender.messages[1])
	}
}

func TestDigestListsRecallsBeforeMaintenance(t *testing.T) {
	now := time.Date(2026, time.March, 18, 12, 0, 0, 0, time.UTC)
	store, roadster, truck := digestFixture(now)
	store.recalls = recallFixture(roadster, truck)[:1]
	sender := &fakeSender{}
	report, err := newTestDigestRunner(store, sender, now).Run(context.Background(), DigestOptions{})
	if err != nil || !report.Sent || report.NewRecalls != 1 || report.Overdue != 1 || report.DueSoon != 2 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	message := sender.messages[0]
	if message.Subject != "Carma weekly digest: 1 new recall, 1 overdue, 2 due soon" {
		t.Fatalf("subject = %q", message.Subject)
	}
	recalls, maintenance, oil := strings.Index(message.Body, "New safety recalls"), strings.Index(message.Body, "\nMaintenance\n"), strings.Index(message.Body, "OVERDUE: Oil change")
	if recalls < 0 || maintenance < recalls || oil < maintenance || !strings.Contains(message.Body, "Log a service record") {
		t.Fatalf("digest order:\n%s", message.Body)
	}
}

func TestDigestSendFailureLeavesRecallsNew(t *testing.T) {
	now := time.Date(2026, time.March, 18, 12, 0, 0, 0, time.UTC)
	store := &fakeDigestStore{lockAvailable: true, emails: []string{"user@example.com"}, recalls: recallFixture(uuid.New(), uuid.New())}
	sender := &fakeSender{err: errors.New("smtp down")}
	runner := newTestDigestRunner(store, sender, now)
	if report, err := runner.Run(context.Background(), DigestOptions{}); err == nil || report.Sent || len(store.notified) != 0 {
		t.Fatalf("report=%+v err=%v notified=%v", report, err, store.notified)
	}
	sender.err = nil
	if report, err := runner.Run(context.Background(), DigestOptions{}); err != nil || !report.Sent || report.NewRecalls != 3 || len(store.notified) != 3 {
		t.Fatalf("retry: report=%+v err=%v notified=%v", report, err, store.notified)
	}
}

func TestDigestDryRunDoesNotMarkRecalls(t *testing.T) {
	now := time.Date(2026, time.March, 18, 12, 0, 0, 0, time.UTC)
	store := &fakeDigestStore{lockAvailable: true, emails: []string{"user@example.com"}, recalls: recallFixture(uuid.New(), uuid.New())}
	report, err := newTestDigestRunner(store, &fakeSender{}, now).Run(context.Background(), DigestOptions{DryRun: true})
	if err != nil || report.Message == nil || report.NewRecalls != 3 || len(store.notified) != 0 || len(store.digests) != 0 {
		t.Fatalf("report=%+v err=%v notified=%v", report, err, store.notified)
	}
}
