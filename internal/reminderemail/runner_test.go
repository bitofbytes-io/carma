package reminderemail

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/mailer"
	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/repository"
	"github.com/google/uuid"
)

type fakeStore struct {
	reminders     []model.Reminder
	emails        []string
	notifications []model.ReminderNotification
	lockAvailable bool
	lockCalls     int
	unlockCalls   int
	auditErr      error
}

func (f *fakeStore) ListReminders(context.Context, *uuid.UUID, bool) ([]model.Reminder, error) {
	return append([]model.Reminder(nil), f.reminders...), nil
}
func (f *fakeStore) ListReminderRecipientEmails(context.Context) ([]string, error) {
	return append([]string(nil), f.emails...), nil
}
func (f *fakeStore) ReminderNotificationSince(_ context.Context, reminderID uuid.UUID, cutoff time.Time) (bool, error) {
	for _, notification := range f.notifications {
		if notification.ReminderID == reminderID && !notification.SentAt.Before(cutoff) {
			return true, nil
		}
	}
	return false, nil
}
func (f *fakeStore) CreateReminderNotification(_ context.Context, notification model.ReminderNotification) error {
	if f.auditErr != nil {
		return f.auditErr
	}
	f.notifications = append(f.notifications, notification)
	return nil
}
func (f *fakeStore) TryReminderEmailLock(context.Context) (func(context.Context) error, bool, error) {
	f.lockCalls++
	if !f.lockAvailable {
		return nil, false, nil
	}
	return func(context.Context) error { f.unlockCalls++; return nil }, true, nil
}

type fakeSender struct {
	messages []mailer.Message
	err      error
}

func (f *fakeSender) Send(_ context.Context, message mailer.Message) (string, error) {
	f.messages = append(f.messages, message)
	if f.err != nil {
		return "", f.err
	}
	return "<test@bitofbytes.io>", nil
}

func TestNormalizeRecipients(t *testing.T) {
	got := NormalizeRecipients([]string{" Daniel@Example.COM ", "daniel@example.com", "Name <other@example.com>", "bad", "two@example.com\nBcc:x@y.com", "a@b.co"})
	want := []string{"daniel@example.com", "a@b.co"}
	if !slices.Equal(got, want) {
		t.Fatalf("recipients = %#v, want %#v", got, want)
	}
}

func TestRunnerSendsOnlyDueAndAuditsAfterSuccess(t *testing.T) {
	now := time.Date(2026, time.March, 15, 12, 0, 0, 0, time.UTC)
	dueID, soonID := uuid.New(), uuid.New()
	month := 1
	store := &fakeStore{lockAvailable: true, emails: []string{" USER@example.com ", "user@example.com"}, reminders: []model.Reminder{
		{ID: dueID, VehicleID: uuid.New(), VehicleName: "Roadster", ServiceTypeName: "Oil change", IntervalMonths: &month, Enabled: true, CreatedAt: now.AddDate(0, -2, 0)},
		{ID: soonID, VehicleID: uuid.New(), VehicleName: "Truck", ServiceTypeName: "Tires", IntervalMonths: &month, Enabled: true, CreatedAt: time.Date(2026, time.February, 20, 0, 0, 0, 0, time.UTC)},
	}}
	sender := &fakeSender{}
	runner := NewRunner(store, sender, "https://carma.bitofbytes.io", slog.New(slog.NewTextHandler(io.Discard, nil)))
	runner.now = func() time.Time { return now }
	report, err := runner.Run(context.Background(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Evaluated != 2 || report.Due != 1 || report.Sent != 1 || report.Failed != 0 {
		t.Fatalf("report = %+v", report)
	}
	if len(sender.messages) != 1 || len(store.notifications) != 1 || store.notifications[0].ReminderID != dueID {
		t.Fatalf("messages=%d notifications=%+v", len(sender.messages), store.notifications)
	}
	if !slices.Equal(sender.messages[0].Recipients, []string{"user@example.com"}) {
		t.Fatalf("envelope recipients = %#v", sender.messages[0].Recipients)
	}
	if store.lockCalls != 1 || store.unlockCalls != 1 {
		t.Fatalf("lock calls=%d unlock=%d", store.lockCalls, store.unlockCalls)
	}
}

func TestRunnerSuppressionBoundaryAndRetryAfterFailure(t *testing.T) {
	now := time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC)
	month, id := 1, uuid.New()
	reminder := model.Reminder{ID: id, VehicleID: uuid.New(), IntervalMonths: &month, Enabled: true, CreatedAt: now.AddDate(0, -2, 0)}
	for _, test := range []struct {
		name         string
		sentAt       time.Time
		wantSent     int
		wantSuppress int
	}{
		{name: "exact boundary suppresses", sentAt: now.Add(-SuppressionWindow), wantSuppress: 1},
		{name: "before boundary sends", sentAt: now.Add(-SuppressionWindow).Add(-time.Nanosecond), wantSent: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeStore{lockAvailable: true, emails: []string{"user@example.com"}, reminders: []model.Reminder{reminder}, notifications: []model.ReminderNotification{{ReminderID: id, SentAt: test.sentAt}}}
			sender := &fakeSender{}
			runner := NewRunner(store, sender, "https://carma.bitofbytes.io", nil)
			runner.now = func() time.Time { return now }
			report, err := runner.Run(context.Background(), Options{})
			if err != nil || report.Sent != test.wantSent || report.Suppressed != test.wantSuppress {
				t.Fatalf("report=%+v err=%v", report, err)
			}
		})
	}
	store := &fakeStore{lockAvailable: true, emails: []string{"user@example.com"}, reminders: []model.Reminder{reminder}}
	sender := &fakeSender{err: errors.New("relay unavailable")}
	runner := NewRunner(store, sender, "https://carma.bitofbytes.io", slog.New(slog.NewTextHandler(io.Discard, nil)))
	runner.now = func() time.Time { return now }
	report, err := runner.Run(context.Background(), Options{})
	if err == nil || report.Failed != 1 || len(store.notifications) != 0 {
		t.Fatalf("report=%+v notifications=%d err=%v", report, len(store.notifications), err)
	}
	sender.err = nil
	report, err = runner.Run(context.Background(), Options{})
	if err != nil || report.Sent != 1 || len(store.notifications) != 1 {
		t.Fatalf("retry report=%+v notifications=%d err=%v", report, len(store.notifications), err)
	}
}

func TestRunnerDryRunAndTargeting(t *testing.T) {
	now := time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC)
	month := 1
	targetID, otherID := uuid.New(), uuid.New()
	store := &fakeStore{lockAvailable: true, emails: []string{"user@example.com"}, reminders: []model.Reminder{
		{ID: targetID, VehicleID: uuid.New(), IntervalMonths: &month, Enabled: true, CreatedAt: now.AddDate(0, -2, 0)},
		{ID: otherID, VehicleID: uuid.New(), IntervalMonths: &month, Enabled: true, CreatedAt: now.AddDate(0, -2, 0)},
	}}
	sender := &fakeSender{}
	runner := NewRunner(store, sender, "https://carma.bitofbytes.io", nil)
	runner.now = func() time.Time { return now }
	report, err := runner.Run(context.Background(), Options{DryRun: true, ReminderID: &targetID})
	if err != nil || report.Evaluated != 1 || report.Due != 1 || report.Sent != 0 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if store.lockCalls != 0 || len(sender.messages) != 0 || len(store.notifications) != 0 {
		t.Fatalf("dry-run mutated: lock=%d sends=%d audits=%d", store.lockCalls, len(sender.messages), len(store.notifications))
	}
	missing := uuid.New()
	if _, err = runner.Run(context.Background(), Options{DryRun: true, ReminderID: &missing}); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("missing target error = %v", err)
	}
}
