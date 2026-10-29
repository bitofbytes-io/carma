package repository

import (
	"context"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/google/uuid"
)

func (p *Postgres) VehicleSpend(ctx context.Context, vehicleID uuid.UUID, today time.Time) (model.SpendSummary, error) {
	// Totals are aggregated in Go so the year window and caps can be tested
	// without a database. The query still drops other vehicles and records
	// that have no cost.
	rows, err := p.pool.Query(ctx, `SELECT st.id,st.name,r.occurred_on,r.cost_cents
		FROM records r JOIN service_types st ON st.id=r.service_type_id
		WHERE r.vehicle_id=$1 AND r.cost_cents IS NOT NULL`, vehicleID)
	if err != nil {
		return model.SpendSummary{}, err
	}
	defer rows.Close()
	var records []model.Record
	for rows.Next() {
		var record model.Record
		var cost int64
		if err := rows.Scan(&record.ServiceTypeID, &record.ServiceTypeName, &record.OccurredOn, &cost); err != nil {
			return model.SpendSummary{}, err
		}
		record.CostCents = new(int64)
		*record.CostCents = cost
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return model.SpendSummary{}, err
	}
	return model.AggregateSpend(records, today), nil
}
