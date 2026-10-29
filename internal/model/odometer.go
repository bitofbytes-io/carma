package model

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ErrInvalidOdometer is returned when mileage is empty, negative, fractional,
// or outside the int64 range.
var ErrInvalidOdometer = errors.New("mileage must be a nonnegative whole number")

// ParseOdometerMiles parses a mileage entry as a nonnegative whole number.
// Surrounding space is ignored. Grouping commas are not: the service-record
// form removes those before calling. Empty input is rejected so a required
// field and an optional one can share the parser; callers skip the call when
// an empty optional field should stay unset.
func ParseOdometerMiles(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	miles, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || miles < 0 {
		return 0, ErrInvalidOdometer
	}
	return miles, nil
}

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
