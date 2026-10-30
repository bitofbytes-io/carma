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
	CurrentOdometer                                 *int64
	// CurrentOdometerOn is the calendar date CurrentOdometer was recorded,
	// stored when the mileage is written so it can feed mileage projections.
	CurrentOdometerOn *time.Time
	LatestOdometer    *int64
	LastRecord        *Record
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
	StartingOdometer             *int64
	StartingOdometerPending      bool
	Enabled                      bool
	// SnoozedUntilOn and SnoozedUntilMiles defer the reminder until every set
	// threshold has passed. The snooze clears when a record becomes the latest
	// match for this reminder.
	SnoozedUntilOn       *time.Time
	SnoozedUntilMiles    *int64
	CreatedAt, UpdatedAt time.Time
	Baseline             *Record
	LatestOdometer       *int64
	// MilesPerDay is the vehicle's estimated driving rate, used to project when
	// a mileage interval comes due. Nil when there is too little history.
	MilesPerDay *float64
	// EstimateHint says what mileage history MilesPerDay still needs when it
	// is nil. Empty means a generic hint.
	EstimateHint string
}

type RecordQuery struct {
	VehicleID     *uuid.UUID
	Search        string
	ServiceTypeID *uuid.UUID
	From, To      *time.Time
	Sort          string
	Desc          bool
}
