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

// MileagePoint is a dated odometer value from a reading, a service record, or
// the vehicle's current mileage.
type MileagePoint struct {
	VehicleID uuid.UUID
	On        time.Time
	Miles     int64
}
