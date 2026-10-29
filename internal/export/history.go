package export

import (
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/google/uuid"
)

// HistorySummary totals a vehicle's service records for the printable history.
type HistorySummary struct {
	Records, Receipts int
	// TotalCents sums the records that have a cost; nil when none do.
	TotalCents *int64
	// First and Last are the earliest and latest service dates; nil when there
	// are no records.
	First, Last *time.Time
}

// Query lists service history oldest first. The printable history and the XLSX
// workbook both start from this query. A nil vehicle ID lists every vehicle.
func Query(vehicleID *uuid.UUID) model.RecordQuery {
	return model.RecordQuery{VehicleID: vehicleID, Sort: "date"}
}

// Summarize totals records for the printable vehicle history.
func Summarize(records []model.Record) HistorySummary {
	summary := HistorySummary{Records: len(records)}
	for i := range records {
		record := &records[i]
		summary.Receipts += record.AttachmentCount
		if record.CostCents != nil {
			total := *record.CostCents
			if summary.TotalCents != nil {
				total = model.AddCents(total, *summary.TotalCents)
			}
			summary.TotalCents = &total
		}
		if summary.First == nil || record.OccurredOn.Before(*summary.First) {
			summary.First = &record.OccurredOn
		}
		if summary.Last == nil || record.OccurredOn.After(*summary.Last) {
			summary.Last = &record.OccurredOn
		}
	}
	return summary
}
