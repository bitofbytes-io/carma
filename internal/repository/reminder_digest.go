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
	// ReminderDigestClaimed reports whether the digest for the week starting on
	// weekStart (a calendar date) has been sent or is being sent.
	ReminderDigestClaimed(ctx context.Context, weekStart time.Time) (bool, error)
	// ReserveReminderDigest claims the digest's week before sending, returning
	// false when the week is already claimed.
	ReserveReminderDigest(context.Context, model.ReminderDigest) (bool, error)
	// CompleteReminderDigest records the accepted message for a claimed week.
	CompleteReminderDigest(ctx context.Context, id uuid.UUID, messageID string, sentAt time.Time) error
	// ReleaseReminderDigest drops a claim whose message was not sent, so a
	// later run can retry the week.
	ReleaseReminderDigest(ctx context.Context, id uuid.UUID) error
	TryReminderDigestLock(context.Context) (unlock func(context.Context) error, acquired bool, err error)
}

func (p *Postgres) ReminderDigestClaimed(ctx context.Context, weekStart time.Time) (bool, error) {
	var exists bool
	err := p.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reminder_digests WHERE week_start=$1::date)`, dateOnly(weekStart)).Scan(&exists)
	return exists, err
}

func (p *Postgres) ReserveReminderDigest(ctx context.Context, digest model.ReminderDigest) (bool, error) {
	tag, err := p.pool.Exec(ctx, `INSERT INTO reminder_digests(id,week_start,reserved_at,recipients)
		VALUES($1,$2::date,$3,$4)
		ON CONFLICT(week_start) DO NOTHING`, digest.ID, dateOnly(digest.WeekStart), digest.ReservedAt.UTC(), digest.Recipients)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (p *Postgres) CompleteReminderDigest(ctx context.Context, id uuid.UUID, messageID string, sentAt time.Time) error {
	tag, err := p.pool.Exec(ctx, `UPDATE reminder_digests SET sent_at=$2,message_id=$3 WHERE id=$1`, id, sentAt.UTC(), messageID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (p *Postgres) ReleaseReminderDigest(ctx context.Context, id uuid.UUID) error {
	_, err := p.pool.Exec(ctx, `DELETE FROM reminder_digests WHERE id=$1 AND sent_at IS NULL`, id)
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
