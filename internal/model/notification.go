package model

import (
	"time"

	"github.com/google/uuid"
)

type ReminderNotification struct {
	ID         uuid.UUID
	ReminderID uuid.UUID
	SentAt     time.Time
	Recipients []string
	MessageID  string
}

// ReminderDigest records the weekly digest email sent for the week starting on
// WeekStart, a Monday in the household time zone.
type ReminderDigest struct {
	ID         uuid.UUID
	WeekStart  time.Time
	SentAt     time.Time
	Recipients []string
	MessageID  string
}
