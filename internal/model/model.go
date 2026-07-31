package model

import (
	"github.com/google/uuid"
	"time"
)

type User struct {
	ID                                                         uuid.UUID
	OAuthProvider, OAuthSubject, Email, DisplayName, AvatarURL string
	CreatedAt, UpdatedAt, LastLoginAt                          time.Time
}
type Session struct {
	ID, UserID           uuid.UUID
	ExpiresAt, CreatedAt time.Time
	UserAgent, IPAddress string
}
type Vehicle struct {
	ID                                              uuid.UUID
	Nickname                                        string
	Year                                            *int
	Make, Model, VIN, LicensePlate, PhotoKey, Notes string
	ArchivedAt                                      *time.Time
	CreatedAt, UpdatedAt                            time.Time
	LatestOdometer                                  *int64
	LastRecord                                      *Record
}
type ServiceType struct {
	ID        uuid.UUID
	Name      string
	Seeded    bool
	CreatedAt time.Time
}
type Record struct {
	ID, VehicleID, ServiceTypeID, CreatedBy     uuid.UUID
	VehicleName, ServiceTypeName, CreatedByName string
	OccurredOn                                  time.Time
	OdometerMiles, CostCents                    *int64
	Vendor, Notes                               string
	CreatedAt, UpdatedAt                        time.Time
	AttachmentCount                             int
}
type Attachment struct {
	ID, RecordID                              uuid.UUID
	OriginalFilename, ContentType, StorageKey string
	ByteSize                                  int64
	CreatedAt                                 time.Time
}
type Reminder struct {
	ID, VehicleID, ServiceTypeID uuid.UUID
	VehicleName, ServiceTypeName string
	IntervalMonths               *int
	IntervalMiles                *int64
	Enabled                      bool
	CreatedAt, UpdatedAt         time.Time
	Baseline                     *Record
	LatestOdometer               *int64
}

type RecordQuery struct {
	VehicleID     *uuid.UUID
	Search        string
	ServiceTypeID *uuid.UUID
	From, To      *time.Time
	Sort          string
	Desc          bool
}
