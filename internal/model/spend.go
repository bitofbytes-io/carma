package model

import "math"

// SpendSummary is what a vehicle's service records cost this year through
// today, and over its whole history. Records without a cost are left out.
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

// AddCents adds two non-negative amounts, capping at math.MaxInt64 instead of
// overflowing when absurdly large costs are entered.
func AddCents(a, b int64) int64 {
	if b > math.MaxInt64-a {
		return math.MaxInt64
	}
	return a + b
}
