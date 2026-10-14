package repository

import (
	"context"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/google/uuid"
)

func (p *Postgres) VehicleSpend(ctx context.Context, vehicleID uuid.UUID, yearStart time.Time) (model.SpendSummary, error) {
	yearEnd := yearStart.AddDate(1, 0, 0)
	rows, err := p.pool.Query(ctx, `SELECT st.name,count(*),
			COALESCE(sum(r.cost_cents) FILTER (WHERE r.occurred_on >= $2 AND r.occurred_on < $3),0)::bigint,
			sum(r.cost_cents)::bigint
		FROM records r JOIN service_types st ON st.id=r.service_type_id
		WHERE r.vehicle_id=$1 AND r.cost_cents IS NOT NULL
		GROUP BY st.id,st.name
		ORDER BY sum(r.cost_cents) DESC,lower(st.name)`, vehicleID, yearStart, yearEnd)
	if err != nil {
		return model.SpendSummary{}, err
	}
	defer rows.Close()
	var summary model.SpendSummary
	for rows.Next() {
		var spend model.ServiceTypeSpend
		if err := rows.Scan(&spend.Name, &spend.Records, &spend.YearToDateCents, &spend.LifetimeCents); err != nil {
			return model.SpendSummary{}, err
		}
		summary.PricedRecords += spend.Records
		summary.YearToDateCents += spend.YearToDateCents
		summary.LifetimeCents += spend.LifetimeCents
		summary.Types = append(summary.Types, spend)
	}
	return summary, rows.Err()
}
