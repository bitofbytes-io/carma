package repository

import (
	"context"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/google/uuid"
)

const initializeReminderOdometerSQL = `UPDATE reminders SET starting_odometer_miles=GREATEST($2::bigint,(SELECT max(odometer_miles) FROM records WHERE vehicle_id=$1)),
			starting_odometer_pending=false
		WHERE vehicle_id=$1
		AND starting_odometer_pending
		AND starting_odometer_miles IS NULL`

const upsertReminderSQL = `INSERT INTO reminders(id,vehicle_id,service_type_id,interval_months,interval_miles,starting_odometer_miles,
			starting_odometer_pending,enabled,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT(vehicle_id,service_type_id)
		DO UPDATE SET interval_months=excluded.interval_months,interval_miles=excluded.interval_miles,enabled=excluded.enabled,
			updated_at=excluded.updated_at
		RETURNING id,created_at,starting_odometer_miles,starting_odometer_pending`

func (p *Postgres) ListReminders(ctx context.Context, vehicleID *uuid.UUID, disabled bool) ([]model.Reminder, error) {
	rows, err := p.pool.Query(ctx, `SELECT rm.id,rm.vehicle_id,rm.service_type_id,v.nickname,st.name,rm.interval_months,rm.interval_miles,
			rm.starting_odometer_miles,rm.starting_odometer_pending,rm.enabled,rm.created_at,rm.updated_at,b.id,
			b.occurred_on,b.odometer_miles,b.created_at,GREATEST(v.current_odometer_miles,(SELECT max(odometer_miles) FROM records x WHERE x.vehicle_id=rm.vehicle_id))
		FROM reminders rm
		JOIN vehicles v ON v.id=rm.vehicle_id
		JOIN service_types st ON st.id=rm.service_type_id
		LEFT JOIN LATERAL (
			SELECT r.id,r.occurred_on,r.odometer_miles,r.created_at
			FROM records r
			WHERE r.vehicle_id=rm.vehicle_id
			AND r.service_type_id=rm.service_type_id
			ORDER BY r.occurred_on DESC,r.created_at DESC,r.id DESC
			LIMIT 1
		) b ON true
		WHERE ($1::uuid IS NULL OR rm.vehicle_id=$1)
		AND ($2 OR rm.enabled)
		AND ($1::uuid IS NOT NULL OR v.archived_at IS NULL)
		ORDER BY lower(v.nickname),lower(st.name)`, vehicleID, disabled)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var reminders []model.Reminder
	for rows.Next() {
		var reminder model.Reminder
		var baselineID *uuid.UUID
		var baselineDate, baselineCreatedAt *time.Time
		var baselineOdometer *int64
		if err := rows.Scan(
			&reminder.ID, &reminder.VehicleID, &reminder.ServiceTypeID,
			&reminder.VehicleName, &reminder.ServiceTypeName, &reminder.IntervalMonths,
			&reminder.IntervalMiles, &reminder.StartingOdometer, &reminder.StartingOdometerPending,
			&reminder.Enabled, &reminder.CreatedAt, &reminder.UpdatedAt,
			&baselineID, &baselineDate, &baselineOdometer,
			&baselineCreatedAt, &reminder.LatestOdometer,
		); err != nil {
			return nil, err
		}
		if baselineID != nil {
			reminder.Baseline = &model.Record{
				ID:            *baselineID,
				VehicleID:     reminder.VehicleID,
				ServiceTypeID: reminder.ServiceTypeID,
				OccurredOn:    *baselineDate,
				OdometerMiles: baselineOdometer,
				CreatedAt:     *baselineCreatedAt,
			}
		}
		reminders = append(reminders, reminder)
	}
	return reminders, rows.Err()
}

func (p *Postgres) UpsertReminder(ctx context.Context, reminder model.Reminder) (model.Reminder, error) {
	err := p.pool.QueryRow(
		ctx, upsertReminderSQL, reminder.ID,
		reminder.VehicleID, reminder.ServiceTypeID, reminder.IntervalMonths,
		reminder.IntervalMiles, reminder.StartingOdometer, reminder.StartingOdometerPending,
		reminder.Enabled, reminder.CreatedAt, reminder.UpdatedAt,
	).Scan(&reminder.ID, &reminder.CreatedAt, &reminder.StartingOdometer, &reminder.StartingOdometerPending)
	return reminder, err
}

func (p *Postgres) DeleteReminder(ctx context.Context, id uuid.UUID) error {
	tag, err := p.pool.Exec(ctx, `DELETE FROM reminders WHERE id=$1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}
