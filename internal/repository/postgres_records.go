package repository

import (
	"context"
	"errors"
	"strings"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (p *Postgres) ListServiceTypes(ctx context.Context) ([]model.ServiceType, error) {
	rows, err := p.pool.Query(ctx, `SELECT id,name,is_seeded,created_at FROM service_types ORDER BY lower(name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var serviceTypes []model.ServiceType
	for rows.Next() {
		var serviceType model.ServiceType
		if err := rows.Scan(&serviceType.ID, &serviceType.Name, &serviceType.Seeded, &serviceType.CreatedAt); err != nil {
			return nil, err
		}
		serviceTypes = append(serviceTypes, serviceType)
	}
	return serviceTypes, rows.Err()
}

func (p *Postgres) CreateServiceType(ctx context.Context, serviceType model.ServiceType) (model.ServiceType, error) {
	err := p.pool.QueryRow(
		ctx, `INSERT INTO service_types(id,name,is_seeded,created_at) VALUES($1,$2,false,$3) RETURNING id,name,is_seeded,created_at`, serviceType.ID,
		serviceType.Name, serviceType.CreatedAt,
	).Scan(&serviceType.ID, &serviceType.Name, &serviceType.Seeded, &serviceType.CreatedAt)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique") {
		err = ErrConflict
	}
	return serviceType, err
}

const recordSelect = `SELECT r.id,r.vehicle_id,r.service_type_id,r.created_by,r.occurred_on,r.odometer_miles,r.cost_cents,r.vendor,
			r.notes,r.created_at,r.updated_at,v.nickname,st.name,COALESCE(NULLIF(u.display_name,''),u.email),(SELECT count(*) FROM attachments a WHERE a.record_id=r.id)
		FROM records r
		JOIN vehicles v ON v.id=r.vehicle_id
		JOIN service_types st ON st.id=r.service_type_id
		JOIN users u ON u.id=r.created_by`

const recordFilter = `
		WHERE ($1::uuid IS NULL OR r.vehicle_id=$1)
		AND ($2::uuid IS NULL OR r.service_type_id=$2)
		AND ($3::date IS NULL OR r.occurred_on >= $3)
		AND ($4::date IS NULL OR r.occurred_on <= $4)
		AND ($5='' OR st.name ILIKE '%'||$5||'%' ESCAPE E'\\' OR r.vendor ILIKE '%'||$5||'%' ESCAPE E'\\' OR r.notes ILIKE '%'||$5||'%' ESCAPE E'\\')`

func scanRecord(row pgx.Row) (model.Record, error) {
	var record model.Record
	err := row.Scan(
		&record.ID, &record.VehicleID, &record.ServiceTypeID,
		&record.CreatedBy, &record.OccurredOn, &record.OdometerMiles,
		&record.CostCents, &record.Vendor, &record.Notes,
		&record.CreatedAt, &record.UpdatedAt, &record.VehicleName,
		&record.ServiceTypeName, &record.CreatedByName, &record.AttachmentCount,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return record, err
}

func (p *Postgres) ListRecords(ctx context.Context, query model.RecordQuery) ([]model.Record, error) {
	order := recordOrder(query)
	sql := recordSelect + recordFilter + ` ORDER BY ` + order
	rows, err := p.pool.Query(
		ctx, sql, query.VehicleID,
		query.ServiceTypeID, query.From, query.To,
		escapeLikePattern(query.Search),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []model.Record
	for rows.Next() {
		record, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func escapeLikePattern(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}

func recordOrder(query model.RecordQuery) string {
	query.Sort, query.Desc = normalizedRecordSort(query.Sort, query.Desc)
	order := "r.occurred_on DESC,r.created_at DESC,r.id DESC"
	direction := "ASC"
	if query.Desc {
		direction = "DESC"
	}
	switch query.Sort {
	case "mileage":
		order = "r.odometer_miles " + direction + " NULLS LAST,r.occurred_on DESC,r.created_at DESC,r.id DESC"
	case "cost":
		order = "r.cost_cents " + direction + " NULLS LAST,r.occurred_on DESC,r.created_at DESC,r.id DESC"
	case "date":
		order = "r.occurred_on " + direction + ",r.created_at DESC,r.id DESC"
	}
	return order
}

func (p *Postgres) GetRecord(ctx context.Context, id uuid.UUID) (model.Record, []model.Attachment, error) {
	record, err := scanRecord(p.pool.QueryRow(ctx, recordSelect+` WHERE r.id=$1`, id))
	if err != nil {
		return record, nil, err
	}
	rows, err := p.pool.Query(ctx, `SELECT id,record_id,original_filename,content_type,byte_size,storage_key,created_at
		FROM attachments
		WHERE record_id=$1
		ORDER BY created_at,id`, id)
	if err != nil {
		return record, nil, err
	}
	defer rows.Close()
	var attachments []model.Attachment
	for rows.Next() {
		var attachment model.Attachment
		if err := rows.Scan(
			&attachment.ID, &attachment.RecordID, &attachment.OriginalFilename,
			&attachment.ContentType, &attachment.ByteSize, &attachment.StorageKey,
			&attachment.CreatedAt,
		); err != nil {
			return record, nil, err
		}
		attachments = append(attachments, attachment)
	}
	return record, attachments, rows.Err()
}

func (p *Postgres) CreateRecord(ctx context.Context, record model.Record, attachments []model.Attachment) (model.Record, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return record, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(
		ctx, `INSERT INTO records(id,vehicle_id,service_type_id,occurred_on,odometer_miles,cost_cents,vendor,notes,
			created_by,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, record.ID,
		record.VehicleID, record.ServiceTypeID, record.OccurredOn,
		record.OdometerMiles, record.CostCents, record.Vendor,
		record.Notes, record.CreatedBy, record.CreatedAt,
		record.UpdatedAt,
	)
	if err != nil {
		return record, err
	}
	for _, attachment := range attachments {
		if _, err = tx.Exec(
			ctx, `INSERT INTO attachments(id,record_id,original_filename,content_type,byte_size,storage_key,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7)`, attachment.ID,
			attachment.RecordID, attachment.OriginalFilename, attachment.ContentType,
			attachment.ByteSize, attachment.StorageKey, attachment.CreatedAt,
		); err != nil {
			return record, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return record, err
	}
	return record, nil
}

func (p *Postgres) UpdateRecord(ctx context.Context, record model.Record) (model.Record, error) {
	tag, err := p.pool.Exec(
		ctx, `UPDATE records SET service_type_id=$2,occurred_on=$3,odometer_miles=$4,cost_cents=$5,vendor=$6,notes=$7,
			updated_at=$8
		WHERE id=$1`, record.ID,
		record.ServiceTypeID, record.OccurredOn, record.OdometerMiles,
		record.CostCents, record.Vendor, record.Notes,
		record.UpdatedAt,
	)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return record, err
}

func (p *Postgres) DeleteRecord(ctx context.Context, id uuid.UUID) ([]string, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `SELECT storage_key FROM attachments WHERE record_id=$1`, id)
	if err != nil {
		return nil, err
	}
	var keys []string
	for rows.Next() {
		var key string
		if err = rows.Scan(&key); err != nil {
			rows.Close()
			return nil, err
		}
		keys = append(keys, key)
	}
	rows.Close()
	tag, err := tx.Exec(ctx, `DELETE FROM records WHERE id=$1`, id)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return keys, nil
}
