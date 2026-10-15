package repository

import (
	"context"
	"errors"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/google/uuid"
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
	// vehicle's mileage. A higher one also dates it.
	var current int64
	if err = tx.QueryRow(
		ctx, `UPDATE vehicles SET current_odometer_miles=GREATEST(current_odometer_miles,$2::bigint),
			current_odometer_on=CASE WHEN current_odometer_miles IS NULL OR $2::bigint>current_odometer_miles
				THEN $3::date ELSE current_odometer_on END
		WHERE id=$1
		RETURNING current_odometer_miles`, reading.VehicleID, reading.OdometerMiles, reading.ReadOn,
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

func (p *Postgres) ListMileagePoints(ctx context.Context, vehicleIDs []uuid.UUID, since time.Time) ([]model.MileagePoint, error) {
	if len(vehicleIDs) == 0 {
		return nil, nil
	}
	ids := make([]string, len(vehicleIDs))
	for i, id := range vehicleIDs {
		ids[i] = id.String()
	}
	rows, err := p.pool.Query(ctx, `SELECT vehicle_id,read_on,odometer_miles FROM odometer_readings
		WHERE vehicle_id=ANY($1::uuid[]) AND read_on>=$2::date
		UNION ALL
		SELECT vehicle_id,occurred_on,odometer_miles FROM records
		WHERE vehicle_id=ANY($1::uuid[]) AND occurred_on>=$2::date AND odometer_miles IS NOT NULL
		UNION ALL
		SELECT v.id,v.current_odometer_on,v.current_odometer_miles FROM vehicles v
		WHERE v.id=ANY($1::uuid[]) AND v.current_odometer_on>=$2::date AND v.current_odometer_miles IS NOT NULL
			AND NOT EXISTS (SELECT 1 FROM odometer_readings o
				WHERE o.vehicle_id=v.id AND o.read_on=v.current_odometer_on AND o.odometer_miles=v.current_odometer_miles)
			AND NOT EXISTS (SELECT 1 FROM records r
				WHERE r.vehicle_id=v.id AND r.occurred_on=v.current_odometer_on AND r.odometer_miles=v.current_odometer_miles)
		ORDER BY 1,2`, ids, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var points []model.MileagePoint
	for rows.Next() {
		var point model.MileagePoint
		if err := rows.Scan(&point.VehicleID, &point.On, &point.Miles); err != nil {
			return nil, err
		}
		points = append(points, point)
	}
	return points, rows.Err()
}
