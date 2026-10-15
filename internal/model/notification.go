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

// ReminderDigest claims the weekly digest email for the week starting on
// WeekStart, a Monday in the household time zone.
type ReminderDigest struct {
	ID         uuid.UUID
	WeekStart  time.Time
	ReservedAt time.Time
	Recipients []string
	// Recalls are the new recall campaigns the digest announces. Reserving the
	// digest marks them notified, and releasing it clears that again.
	Recalls []RecallKey
}
