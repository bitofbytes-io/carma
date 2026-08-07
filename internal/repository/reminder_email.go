package repository

import (
	"context"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/google/uuid"
)

const reminderEmailAdvisoryLockID int64 = 0x4341524d41454d4c // "CARMAEML"
const reminderLockAcquireTimeout = advisoryLockAcquireTimeout

type ReminderEmailStore interface {
	ListReminders(context.Context, *uuid.UUID, bool) ([]model.Reminder, error)
	ListReminderRecipientEmails(context.Context) ([]string, error)
	ReminderNotificationSince(context.Context, uuid.UUID, time.Time) (bool, error)
	CreateReminderNotification(context.Context, model.ReminderNotification) error
	TryReminderEmailLock(context.Context) (unlock func(context.Context) error, acquired bool, err error)
}

func (p *Postgres) ListReminderRecipientEmails(ctx context.Context) ([]string, error) {
	rows, err := p.pool.Query(ctx, `SELECT email FROM users ORDER BY lower(email), email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var emails []string
	for rows.Next() {
		var email string
		if err = rows.Scan(&email); err != nil {
			return nil, err
		}
		emails = append(emails, email)
	}
	return emails, rows.Err()
}

func (p *Postgres) ReminderNotificationSince(ctx context.Context, reminderID uuid.UUID, cutoff time.Time) (bool, error) {
	var exists bool
	err := p.pool.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM reminder_notifications WHERE reminder_id=$1 AND sent_at >= $2
	)`, reminderID, cutoff.UTC()).Scan(&exists)
	return exists, err
}

func (p *Postgres) CreateReminderNotification(ctx context.Context, notification model.ReminderNotification) error {
	_, err := p.pool.Exec(ctx, `INSERT INTO reminder_notifications(id,reminder_id,sent_at,recipients,message_id)
		VALUES($1,$2,$3,$4,$5)`, notification.ID, notification.ReminderID, notification.SentAt.UTC(), notification.Recipients, notification.MessageID)
	return err
}

func (p *Postgres) TryReminderEmailLock(ctx context.Context) (func(context.Context) error, bool, error) {
	return p.tryAdvisoryLock(ctx, reminderEmailAdvisoryLockID, "reminder")
}

func boundedReminderLockAcquireContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return boundedAdvisoryLockAcquireContext(ctx)
}
