package model

import (
	"time"

	"github.com/google/uuid"
)

// CalendarFeed is the household's calendar subscription link. Only the
// SHA-256 hash of its token is kept.
type CalendarFeed struct {
	TokenHash []byte
	CreatedBy *uuid.UUID
	CreatedAt time.Time
}
