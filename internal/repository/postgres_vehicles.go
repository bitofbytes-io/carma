package repository

import (
	"context"
	"errors"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const vehicleCols = `v.id,v.nickname,v.year,v.make,v.model,v.vin,v.license_plate,v.photo_key,v.notes,v.archived_at,v.created_at,v.updated_at,v.current_odometer_miles`

func scanVehicle(row pgx.Row) (model.Vehicle, error) {
	var vehicle model.Vehicle
	err := row.Scan(
		&vehicle.ID, &vehicle.Nickname, &vehicle.Year,
		&vehicle.Make, &vehicle.Model, &vehicle.VIN,
		&vehicle.LicensePlate, &vehicle.PhotoKey, &vehicle.Notes,
		&vehicle.ArchivedAt, &vehicle.CreatedAt, &vehicle.UpdatedAt,
		&vehicle.CurrentOdometer, &vehicle.LatestOdometer,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return vehicle, err
}

func (p *Postgres) ListVehicles(ctx context.Context, archived bool) ([]model.Vehicle, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+vehicleCols+`,
		GREATEST(v.current_odometer_miles,(SELECT max(odometer_miles) FROM records WHERE vehicle_id=v.id)),
		lr.id,lr.vehicle_id,lr.service_type_id,lr.created_by,lr.occurred_on,lr.odometer_miles,lr.cost_cents,
			lr.vendor,lr.notes,lr.created_at,lr.updated_at,lr.service_type_name
		FROM vehicles v
		LEFT JOIN LATERAL (
			SELECT r.id,r.vehicle_id,r.service_type_id,r.created_by,r.occurred_on,r.odometer_miles,r.cost_cents,
			r.vendor,r.notes,r.created_at,r.updated_at,st.name AS service_type_name
			FROM records r
			JOIN service_types st ON st.id=r.service_type_id
			WHERE r.vehicle_id=v.id
			ORDER BY r.occurred_on DESC,r.created_at DESC,r.id DESC
			LIMIT 1
		) lr ON true
		WHERE (v.archived_at IS NOT NULL)=$1
		ORDER BY lower(v.nickname)`, archived)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var vehicles []model.Vehicle
	for rows.Next() {
		var vehicle model.Vehicle
		var recordID, recordVehicleID, typeID, createdBy *uuid.UUID
		var occurred, created, updated *time.Time
		var odometer, cost *int64
		var vendor, notes, typeName *string
		err = rows.Scan(
			&vehicle.ID, &vehicle.Nickname, &vehicle.Year,
			&vehicle.Make, &vehicle.Model, &vehicle.VIN,
			&vehicle.LicensePlate, &vehicle.PhotoKey, &vehicle.Notes,
			&vehicle.ArchivedAt, &vehicle.CreatedAt, &vehicle.UpdatedAt,
			&vehicle.CurrentOdometer, &vehicle.LatestOdometer, &recordID,
			&recordVehicleID, &typeID, &createdBy,
			&occurred, &odometer, &cost,
			&vendor, &notes, &created,
			&updated, &typeName,
		)
		if err != nil {
			return nil, err
		}
		if recordID != nil {
			vehicle.LastRecord = &model.Record{
				ID:              *recordID,
				VehicleID:       *recordVehicleID,
				ServiceTypeID:   *typeID,
				CreatedBy:       *createdBy,
				OccurredOn:      *occurred,
				OdometerMiles:   odometer,
				CostCents:       cost,
				Vendor:          *vendor,
				Notes:           *notes,
				CreatedAt:       *created,
				UpdatedAt:       *updated,
				ServiceTypeName: *typeName,
			}
		}
		vehicles = append(vehicles, vehicle)
	}
	return vehicles, rows.Err()
}

func (p *Postgres) GetVehicle(ctx context.Context, id uuid.UUID) (model.Vehicle, error) {
	return scanVehicle(p.pool.QueryRow(ctx, `SELECT `+vehicleCols+`,GREATEST(v.current_odometer_miles,(SELECT max(odometer_miles) FROM records WHERE vehicle_id=v.id))
		FROM vehicles v
		WHERE v.id=$1`, id))
}

func (p *Postgres) CreateVehicle(ctx context.Context, vehicle model.Vehicle) (model.Vehicle, error) {
	err := p.pool.QueryRow(
		ctx, `INSERT INTO vehicles(id,nickname,year,make,model,vin,license_plate,photo_key,notes,current_odometer_miles,
			created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		RETURNING id,nickname,year,make,model,vin,license_plate,photo_key,notes,archived_at,created_at,updated_at,
			current_odometer_miles`, vehicle.ID,
		vehicle.Nickname, vehicle.Year, vehicle.Make,
		vehicle.Model, vehicle.VIN, vehicle.LicensePlate,
		vehicle.PhotoKey, vehicle.Notes, vehicle.CurrentOdometer,
		vehicle.CreatedAt, vehicle.UpdatedAt,
	).Scan(
		&vehicle.ID, &vehicle.Nickname, &vehicle.Year,
		&vehicle.Make, &vehicle.Model, &vehicle.VIN,
		&vehicle.LicensePlate, &vehicle.PhotoKey, &vehicle.Notes,
		&vehicle.ArchivedAt, &vehicle.CreatedAt, &vehicle.UpdatedAt,
		&vehicle.CurrentOdometer,
	)
	vehicle.LatestOdometer = cloneInt64(vehicle.CurrentOdometer)
	return vehicle, err
}

func (p *Postgres) UpdateVehicle(ctx context.Context, vehicle model.Vehicle) (model.Vehicle, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return vehicle, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(
		ctx, `UPDATE vehicles SET nickname=$2,year=$3,make=$4,model=$5,vin=$6,license_plate=$7,photo_key=$8,notes=$9,
			current_odometer_miles=$10,updated_at=$11
		WHERE id=$1`, vehicle.ID,
		vehicle.Nickname, vehicle.Year, vehicle.Make,
		vehicle.Model, vehicle.VIN, vehicle.LicensePlate,
		vehicle.PhotoKey, vehicle.Notes, vehicle.CurrentOdometer,
		vehicle.UpdatedAt,
	)
	if err != nil {
		return vehicle, err
	}
	if tag.RowsAffected() == 0 {
		return vehicle, ErrNotFound
	}
	if vehicle.CurrentOdometer != nil {
		if _, err = tx.Exec(ctx, initializeReminderOdometerSQL, vehicle.ID, vehicle.CurrentOdometer); err != nil {
			return vehicle, err
		}
	}
	updated, err := scanVehicle(tx.QueryRow(ctx, `SELECT `+vehicleCols+`,GREATEST(v.current_odometer_miles,(SELECT max(odometer_miles) FROM records WHERE vehicle_id=v.id))
		FROM vehicles v
		WHERE v.id=$1`, vehicle.ID))
	if err != nil {
		return vehicle, err
	}
	if err = tx.Commit(ctx); err != nil {
		return vehicle, err
	}
	return updated, nil
}

func (p *Postgres) ArchiveVehicle(ctx context.Context, id uuid.UUID) error {
	tag, err := p.pool.Exec(ctx, `UPDATE vehicles SET archived_at=now(),updated_at=now() WHERE id=$1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}
