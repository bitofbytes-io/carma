package repository

import (
	"context"
	"errors"
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

	ListVehicles(context.Context, bool) ([]model.Vehicle, error)
	GetVehicle(context.Context, uuid.UUID) (model.Vehicle, error)
	CreateVehicle(context.Context, model.Vehicle) (model.Vehicle, error)
	UpdateVehicle(context.Context, model.Vehicle) (model.Vehicle, error)
	ArchiveVehicle(context.Context, uuid.UUID) error

	ListServiceTypes(context.Context) ([]model.ServiceType, error)
	CreateServiceType(context.Context, model.ServiceType) (model.ServiceType, error)

	ListRecords(context.Context, model.RecordQuery) ([]model.Record, error)
	GetRecord(context.Context, uuid.UUID) (model.Record, []model.Attachment, error)
	CreateRecord(context.Context, model.Record, []model.Attachment) (model.Record, error)
	UpdateRecord(context.Context, model.Record) (model.Record, error)
	DeleteRecord(context.Context, uuid.UUID) ([]string, error)
	AddAttachments(context.Context, uuid.UUID, []model.Attachment) error
	DeleteAttachment(context.Context, uuid.UUID) (string, error)

	ListReminders(context.Context, *uuid.UUID, bool) ([]model.Reminder, error)
	UpsertReminder(context.Context, model.Reminder) (model.Reminder, error)
	DeleteReminder(context.Context, uuid.UUID) error
	Close()
}
