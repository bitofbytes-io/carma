package model

// SpendSummary is what a vehicle's service records cost, this calendar year
// and over its whole history. Records without a cost are left out.
type SpendSummary struct {
	YearToDateCents, LifetimeCents int64
	// PricedRecords counts the records that have a cost.
	PricedRecords int
	// Types breaks the totals down by service type, highest lifetime cost first.
	Types []ServiceTypeSpend
}

type ServiceTypeSpend struct {
	Name                           string
	Records                        int
	YearToDateCents, LifetimeCents int64
}
