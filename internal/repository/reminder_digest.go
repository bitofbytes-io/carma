package repository

import (
	"context"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/google/uuid"
)

const reminderDigestAdvisoryLockID int64 = 0x4341524d41444754 // "CARMADGT"

type ReminderDigestStore interface {
	ListReminders(context.Context, *uuid.UUID, bool) ([]model.Reminder, error)
	ListMileagePoints(ctx context.Context, vehicleIDs []uuid.UUID, since time.Time) ([]model.MileagePoint, error)
	ListReminderRecipientEmails(context.Context) ([]string, error)
	// ReminderDigestSent reports whether the digest for the week starting on
	// weekStart (a calendar date) has been sent.
	ReminderDigestSent(ctx context.Context, weekStart time.Time) (bool, error)
	CreateReminderDigest(context.Context, model.ReminderDigest) error
	TryReminderDigestLock(context.Context) (unlock func(context.Context) error, acquired bool, err error)
}

func (p *Postgres) ReminderDigestSent(ctx context.Context, weekStart time.Time) (bool, error) {
	var exists bool
	err := p.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reminder_digests WHERE week_start=$1::date)`, dateOnly(weekStart)).Scan(&exists)
	return exists, err
}

func (p *Postgres) CreateReminderDigest(ctx context.Context, digest model.ReminderDigest) error {
	_, err := p.pool.Exec(ctx, `INSERT INTO reminder_digests(id,week_start,sent_at,recipients,message_id)
		VALUES($1,$2::date,$3,$4,$5)`, digest.ID, dateOnly(digest.WeekStart), digest.SentAt.UTC(), digest.Recipients, digest.MessageID)
	return err
}

func (p *Postgres) TryReminderDigestLock(ctx context.Context) (func(context.Context) error, bool, error) {
	return p.tryAdvisoryLock(ctx, reminderDigestAdvisoryLockID, "reminder digest")
}

// dateOnly formats t's calendar date in its own location, so a local Monday is
// stored as that Monday whatever the session time zone.
func dateOnly(t time.Time) string {
	return t.Format("2006-01-02")
}
