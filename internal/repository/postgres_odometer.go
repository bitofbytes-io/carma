package repository

import (
	"context"
	"errors"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/jackc/pgx/v5"
)

// ErrOdometerBelowLatest reports an unconfirmed reading lower than the
// vehicle's latest known mileage.
var ErrOdometerBelowLatest = errors.New("odometer reading is below the latest mileage")

func (p *Postgres) AddOdometerReading(ctx context.Context, reading model.OdometerReading, allowLower bool) (model.Vehicle, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return model.Vehicle{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Lock the vehicle so concurrent check-ins are compared against each other.
	var locked int
	err = tx.QueryRow(ctx, `SELECT 1 FROM vehicles WHERE id=$1 FOR UPDATE`, reading.VehicleID).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Vehicle{}, ErrNotFound
	}
	if err != nil {
		return model.Vehicle{}, err
	}
	vehicle, err := scanVehicle(tx.QueryRow(ctx, selectVehicleSQL, reading.VehicleID))
	if err != nil {
		return vehicle, err
	}
	if !allowLower && vehicle.LatestOdometer != nil && reading.OdometerMiles < *vehicle.LatestOdometer {
		return vehicle, ErrOdometerBelowLatest
	}
	if _, err = tx.Exec(
		ctx, `INSERT INTO odometer_readings(id,vehicle_id,read_on,odometer_miles,created_by,created_at)
		VALUES($1,$2,$3,$4,$5,$6)`, reading.ID,
		reading.VehicleID, reading.ReadOn, reading.OdometerMiles,
		reading.CreatedBy, reading.CreatedAt,
	); err != nil {
		return vehicle, err
	}
	// A confirmed lower reading stays in the history but never lowers the
	// vehicle's mileage.
	var current int64
	if err = tx.QueryRow(
		ctx, `UPDATE vehicles SET current_odometer_miles=GREATEST(current_odometer_miles,$2::bigint)
		WHERE id=$1
		RETURNING current_odometer_miles`, reading.VehicleID, reading.OdometerMiles,
	).Scan(&current); err != nil {
		return vehicle, err
	}
	if _, err = tx.Exec(ctx, initializeReminderOdometerSQL, reading.VehicleID, current); err != nil {
		return vehicle, err
	}
	updated, err := scanVehicle(tx.QueryRow(ctx, selectVehicleSQL, reading.VehicleID))
	if err != nil {
		return vehicle, err
	}
	if err = tx.Commit(ctx); err != nil {
		return vehicle, err
	}
	return updated, nil
}
