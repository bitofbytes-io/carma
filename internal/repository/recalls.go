package repository

import (
	"context"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const recallCheckAdvisoryLockID int64 = 0x4341524d4152434c // "CARMARCL"

// RecallCheckStore is what the weekly NHTSA recall check needs.
type RecallCheckStore interface {
	// ListRecallCheckDue returns active vehicles with a year, make, and model
	// that were never checked, were last checked before staleBefore, or were
	// checked for a different year, make, or model.
	ListRecallCheckDue(ctx context.Context, staleBefore time.Time) ([]model.Vehicle, error)
	// SaveRecallCheck replaces the vehicle's campaigns with recalls, checked for
	// its year, make, and model. Campaigns still listed keep their first-seen
	// time, dismissal, and notification. It returns how many campaigns are new.
	//
	// The first check for a year, make, and model is the vehicle's baseline:
	// its campaigns are shown on the vehicle page but count as already
	// notified, so only campaigns NHTSA adds later are emailed.
	SaveRecallCheck(ctx context.Context, vehicle model.Vehicle, recalls []model.Recall, checkedAt time.Time) (added int, err error)
	TryRecallCheckLock(context.Context) (unlock func(context.Context) error, acquired bool, err error)
}

func (p *Postgres) TryRecallCheckLock(ctx context.Context) (func(context.Context) error, bool, error) {
	return p.tryAdvisoryLock(ctx, recallCheckAdvisoryLockID, "recall check")
}

func (p *Postgres) ListRecallCheckDue(ctx context.Context, staleBefore time.Time) ([]model.Vehicle, error) {
	rows, err := p.pool.Query(ctx, `SELECT v.id,v.nickname,v.year,v.make,v.model
		FROM vehicles v
		LEFT JOIN vehicle_recall_checks c ON c.vehicle_id=v.id
		WHERE v.archived_at IS NULL AND v.year IS NOT NULL AND btrim(v.make)<>'' AND btrim(v.model)<>''
			AND (c.vehicle_id IS NULL OR c.checked_at<$1 OR c.model_year<>v.year OR c.make<>v.make OR c.model<>v.model)
		ORDER BY c.checked_at NULLS FIRST, v.id`, staleBefore.UTC())
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (model.Vehicle, error) {
		var vehicle model.Vehicle
		err := row.Scan(&vehicle.ID, &vehicle.Nickname, &vehicle.Year, &vehicle.Make, &vehicle.Model)
		return vehicle, err
	})
}

func (p *Postgres) SaveRecallCheck(ctx context.Context, vehicle model.Vehicle, recalls []model.Recall, checkedAt time.Time) (int, error) {
	if vehicle.Year == nil {
		return 0, ErrNotFound
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var covered bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM vehicle_recall_checks
		WHERE vehicle_id=$1 AND model_year=$2 AND make=$3 AND model=$4)`,
		vehicle.ID, *vehicle.Year, vehicle.Make, vehicle.Model).Scan(&covered); err != nil {
		return 0, err
	}
	var notifiedAt *time.Time
	if !covered {
		baseline := checkedAt.UTC()
		notifiedAt = &baseline
	}
	campaigns := make([]string, 0, len(recalls))
	for _, recall := range recalls {
		campaigns = append(campaigns, recall.CampaignNumber)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM vehicle_recalls WHERE vehicle_id=$1 AND NOT (campaign_number=ANY($2))`, vehicle.ID, campaigns); err != nil {
		return 0, err
	}
	added := 0
	for _, recall := range recalls {
		var inserted bool
		// xmax is 0 only for a row this statement inserted rather than updated.
		if err = tx.QueryRow(ctx, `INSERT INTO vehicle_recalls(vehicle_id,campaign_number,component,summary,consequence,remedy,report_received_on,park_it,park_outside,first_seen_at,notified_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
			ON CONFLICT(vehicle_id,campaign_number) DO UPDATE SET
				component=EXCLUDED.component,summary=EXCLUDED.summary,consequence=EXCLUDED.consequence,remedy=EXCLUDED.remedy,
				report_received_on=EXCLUDED.report_received_on,park_it=EXCLUDED.park_it,park_outside=EXCLUDED.park_outside
			RETURNING xmax=0`,
			vehicle.ID, recall.CampaignNumber, recall.Component, recall.Summary, recall.Consequence, recall.Remedy,
			recall.ReportReceivedOn, recall.ParkIt, recall.ParkOutside, checkedAt.UTC(), notifiedAt,
		).Scan(&inserted); err != nil {
			return 0, err
		}
		if inserted {
			added++
		}
	}
	tag, err := tx.Exec(ctx, `INSERT INTO vehicle_recall_checks(vehicle_id,checked_at,model_year,make,model)
		SELECT id,$2,$3,$4,$5 FROM vehicles WHERE id=$1
		ON CONFLICT(vehicle_id) DO UPDATE SET checked_at=EXCLUDED.checked_at,model_year=EXCLUDED.model_year,make=EXCLUDED.make,model=EXCLUDED.model`,
		vehicle.ID, checkedAt.UTC(), *vehicle.Year, vehicle.Make, vehicle.Model)
	if err != nil {
		return 0, err
	}
	if tag.RowsAffected() == 0 {
		return 0, ErrNotFound
	}
	return added, tx.Commit(ctx)
}

// ListRecalls returns recall campaigns for active vehicles, or for one vehicle
// when vehicleID is set, newest report first. Campaigns found for a year, make,
// or model the vehicle no longer has are left out until it is checked again.
func (p *Postgres) ListRecalls(ctx context.Context, vehicleID *uuid.UUID, includeDismissed bool) ([]model.Recall, error) {
	rows, err := p.pool.Query(ctx, `SELECT r.vehicle_id,v.nickname,r.campaign_number,r.component,r.summary,r.consequence,r.remedy,
			r.report_received_on,r.park_it,r.park_outside,r.first_seen_at,r.dismissed_at
		FROM vehicle_recalls r
		JOIN vehicles v ON v.id=r.vehicle_id
		JOIN vehicle_recall_checks c ON c.vehicle_id=r.vehicle_id
		WHERE ($1::uuid IS NULL OR r.vehicle_id=$1) AND ($2 OR r.dismissed_at IS NULL)
			AND ($1::uuid IS NOT NULL OR v.archived_at IS NULL)
			AND c.model_year=v.year AND c.make=v.make AND c.model=v.model
		ORDER BY lower(v.nickname),v.id,r.report_received_on DESC NULLS LAST,r.campaign_number`, vehicleID, includeDismissed)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (model.Recall, error) {
		var recall model.Recall
		err := row.Scan(&recall.VehicleID, &recall.VehicleName, &recall.CampaignNumber, &recall.Component, &recall.Summary,
			&recall.Consequence, &recall.Remedy, &recall.ReportReceivedOn, &recall.ParkIt, &recall.ParkOutside,
			&recall.FirstSeenAt, &recall.DismissedAt)
		return recall, err
	})
}

// ListUnnotifiedRecalls returns the open recall campaigns on active vehicles
// that no digest has announced yet, in ListRecalls order, with each vehicle's
// VIN.
func (p *Postgres) ListUnnotifiedRecalls(ctx context.Context) ([]model.Recall, error) {
	rows, err := p.pool.Query(ctx, `SELECT r.vehicle_id,v.nickname,v.vin,r.campaign_number,r.component,r.summary,r.consequence,r.remedy,
			r.report_received_on,r.park_it,r.park_outside,r.first_seen_at
		FROM vehicle_recalls r
		JOIN vehicles v ON v.id=r.vehicle_id
		JOIN vehicle_recall_checks c ON c.vehicle_id=r.vehicle_id
		WHERE r.notified_at IS NULL AND r.dismissed_at IS NULL AND v.archived_at IS NULL
			AND c.model_year=v.year AND c.make=v.make AND c.model=v.model
		ORDER BY lower(v.nickname),v.id,r.report_received_on DESC NULLS LAST,r.campaign_number`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (model.Recall, error) {
		var recall model.Recall
		err := row.Scan(&recall.VehicleID, &recall.VehicleName, &recall.VehicleVIN, &recall.CampaignNumber, &recall.Component, &recall.Summary,
			&recall.Consequence, &recall.Remedy, &recall.ReportReceivedOn, &recall.ParkIt, &recall.ParkOutside, &recall.FirstSeenAt)
		return recall, err
	})
}

func (p *Postgres) SetRecallDismissed(ctx context.Context, vehicleID uuid.UUID, campaignNumber string, dismissedAt *time.Time) error {
	tag, err := p.pool.Exec(ctx, `UPDATE vehicle_recalls SET dismissed_at=$3 WHERE vehicle_id=$1 AND campaign_number=$2`, vehicleID, campaignNumber, dismissedAt)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}
