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
