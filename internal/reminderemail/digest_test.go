package reminderemail

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/google/uuid"
)

type fakeDigestStore struct {
	reminders     []model.Reminder
	points        []model.MileagePoint
	emails        []string
	digests       []model.ReminderDigest
	lockAvailable bool
	lockCalls     int
	unlockCalls   int
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
func (f *fakeDigestStore) ReminderDigestSent(_ context.Context, weekStart time.Time) (bool, error) {
	for _, digest := range f.digests {
		if digest.WeekStart.Equal(weekStart) {
			return true, nil
		}
	}
	return false, nil
}
func (f *fakeDigestStore) CreateReminderDigest(_ context.Context, digest model.ReminderDigest) error {
	f.digests = append(f.digests, digest)
	return nil
}
func (f *fakeDigestStore) TryReminderDigestLock(context.Context) (func(context.Context) error, bool, error) {
	f.lockCalls++
	if !f.lockAvailable {
		return nil, false, nil
	}
	return func(context.Context) error { f.unlockCalls++; return nil }, true, nil
}

func newTestDigestRunner(store *fakeDigestStore, sender *fakeSender, now time.Time) *DigestRunner {
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
