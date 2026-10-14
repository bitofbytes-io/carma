package repository

import (
	"context"
	"errors"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/google/uuid"
)

var ErrNotFound = errors.New("not found")
var ErrConflict = errors.New("already exists")

type Store interface {
	FindUserByOAuth(context.Context, string, string) (*model.User, error)
	FindUserByEmail(context.Context, string) (*model.User, error)
	UpsertUser(context.Context, model.User) (model.User, error)
	CreateSession(context.Context, model.Session, string) error
	FindSession(context.Context, string) (*model.Session, *model.User, error)
	DeleteSession(context.Context, uuid.UUID) error
	// DeleteExpiredSessions removes sessions whose expiry is at or before now.
	DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error)

	ListVehicles(context.Context, bool) ([]model.Vehicle, error)
	GetVehicle(context.Context, uuid.UUID) (model.Vehicle, error)
	// CreateVehicle saves a new vehicle and its starter reminders in one
	// transaction; each reminder is attached to the vehicle's ID.
	CreateVehicle(ctx context.Context, vehicle model.Vehicle, reminders []model.Reminder) (model.Vehicle, error)
	// UpdateVehicle saves the vehicle's fields. Its PhotoKey is a newly uploaded
	// photo, or empty to keep the current one; when a new photo replaces an
	// existing one, the replaced key is returned so its object can be deleted.
	UpdateVehicle(context.Context, model.Vehicle) (updated model.Vehicle, replacedPhotoKey string, err error)
	ArchiveVehicle(context.Context, uuid.UUID) error
	// AddOdometerReading stores a mileage check-in and raises the vehicle's
	// current odometer to it, returning the updated vehicle. A reading below the
	// latest known mileage fails with ErrOdometerBelowLatest unless allowLower
	// is set; it is then kept in the history without lowering the mileage.
	AddOdometerReading(ctx context.Context, reading model.OdometerReading, allowLower bool) (model.Vehicle, error)
	// ListMileagePoints returns the vehicles' odometer readings and service
	// record mileages dated on or after since, ordered by vehicle and date.
	ListMileagePoints(ctx context.Context, vehicleIDs []uuid.UUID, since time.Time) ([]model.MileagePoint, error)

	ListServiceTypes(context.Context) ([]model.ServiceType, error)
	CreateServiceType(context.Context, model.ServiceType) (model.ServiceType, error)

	ListRecords(context.Context, model.RecordQuery) ([]model.Record, error)
	GetRecord(context.Context, uuid.UUID) (model.Record, []model.Attachment, error)
	GetAttachment(context.Context, uuid.UUID) (model.Record, model.Attachment, error)
	CreateRecord(context.Context, model.Record, []model.Attachment) (model.Record, error)
	UpdateRecord(context.Context, model.Record) (model.Record, error)
	DeleteRecord(context.Context, uuid.UUID) ([]string, error)
	AddAttachments(context.Context, uuid.UUID, []model.Attachment) error
	DeleteAttachment(context.Context, uuid.UUID) (string, error)

	// VehicleSpend totals a vehicle's record costs for the calendar year that
	// starts on yearStart and for all time, overall and by service type.
	VehicleSpend(ctx context.Context, vehicleID uuid.UUID, yearStart time.Time) (model.SpendSummary, error)

	ListReminders(context.Context, *uuid.UUID, bool) ([]model.Reminder, error)
	UpsertReminder(context.Context, model.Reminder) (model.Reminder, error)
	DeleteReminder(context.Context, uuid.UUID) error

	// ListRecalls returns NHTSA recall campaigns for active vehicles, or for one
	// vehicle when vehicleID is set, skipping dismissed ones unless asked.
	ListRecalls(ctx context.Context, vehicleID *uuid.UUID, includeDismissed bool) ([]model.Recall, error)
	// SetRecallDismissed hides a vehicle's recall campaign, or shows it again
	// when dismissedAt is nil.
	SetRecallDismissed(ctx context.Context, vehicleID uuid.UUID, campaignNumber string, dismissedAt *time.Time) error
	Close()
}

func normalizedRecordSort(field string, descending bool) (string, bool) {
	switch field {
	case "date", "mileage", "cost":
		return field, descending
	default:
		return "", true
	}
}
