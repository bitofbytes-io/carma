package repository

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/google/uuid"
)

const reminderEmailAdvisoryLockID int64 = 0x4341524d41454d4c // "CARMAEML"

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
	connection, err := p.pool.Acquire(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("acquire reminder lock connection: %w", err)
	}
	var acquired bool
	if err = connection.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, reminderEmailAdvisoryLockID).Scan(&acquired); err != nil {
		connection.Release()
		return nil, false, fmt.Errorf("acquire reminder advisory lock: %w", err)
	}
	if !acquired {
		connection.Release()
		return nil, false, nil
	}
	var once sync.Once
	var unlockErr error
	unlock := func(unlockContext context.Context) error {
		once.Do(func() {
			defer connection.Release()
			var unlocked bool
			if err := connection.QueryRow(unlockContext, `SELECT pg_advisory_unlock($1)`, reminderEmailAdvisoryLockID).Scan(&unlocked); err != nil {
				unlockErr = fmt.Errorf("release reminder advisory lock: %w", err)
			} else if !unlocked {
				unlockErr = fmt.Errorf("release reminder advisory lock: lock was not held")
			}
		})
		return unlockErr
	}
	return unlock, true, nil
}
