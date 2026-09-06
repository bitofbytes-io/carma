package repository

import (
	"context"
	"errors"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (p *Postgres) GetAttachment(ctx context.Context, id uuid.UUID) (model.Record, model.Attachment, error) {
	row := p.pool.QueryRow(ctx, `SELECT r.id,r.vehicle_id,r.service_type_id,r.created_by,r.occurred_on,r.odometer_miles,r.cost_cents,r.vendor,
			r.notes,r.created_at,r.updated_at,v.nickname,st.name,COALESCE(NULLIF(u.display_name,''),u.email),(SELECT count(*) FROM attachments x WHERE x.record_id=r.id),a.id,a.record_id,a.original_filename,a.content_type,a.byte_size,a.storage_key,
			a.created_at
		FROM attachments a
		JOIN records r ON r.id=a.record_id
		JOIN vehicles v ON v.id=r.vehicle_id
		JOIN service_types st ON st.id=r.service_type_id
		JOIN users u ON u.id=r.created_by
		WHERE a.id=$1`, id)
	var record model.Record
	var attachment model.Attachment
	err := row.Scan(
		&record.ID, &record.VehicleID, &record.ServiceTypeID,
		&record.CreatedBy, &record.OccurredOn, &record.OdometerMiles,
		&record.CostCents, &record.Vendor, &record.Notes,
		&record.CreatedAt, &record.UpdatedAt, &record.VehicleName,
		&record.ServiceTypeName, &record.CreatedByName, &record.AttachmentCount,
		&attachment.ID, &attachment.RecordID, &attachment.OriginalFilename,
		&attachment.ContentType, &attachment.ByteSize, &attachment.StorageKey,
		&attachment.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Record{}, model.Attachment{}, ErrNotFound
	}
	return record, attachment, err
}

func (p *Postgres) AddAttachments(ctx context.Context, recordID uuid.UUID, attachments []model.Attachment) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, attachment := range attachments {
		if _, err = tx.Exec(
			ctx, `INSERT INTO attachments(id,record_id,original_filename,content_type,byte_size,storage_key,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7)`, attachment.ID,
			recordID, attachment.OriginalFilename, attachment.ContentType,
			attachment.ByteSize, attachment.StorageKey, attachment.CreatedAt,
		); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (p *Postgres) DeleteAttachment(ctx context.Context, id uuid.UUID) (string, error) {
	var key string
	err := p.pool.QueryRow(ctx, `DELETE FROM attachments WHERE id=$1 RETURNING storage_key`, id).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return key, err
}
