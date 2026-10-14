package model

import (
	"time"

	"github.com/google/uuid"
)

// OdometerReading is one mileage check-in for a vehicle.
type OdometerReading struct {
	ID, VehicleID, CreatedBy uuid.UUID
	ReadOn                   time.Time
	OdometerMiles            int64
	CreatedAt                time.Time
}
