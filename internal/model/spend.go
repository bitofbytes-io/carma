package model

import (
	"errors"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

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

// ErrInvalidCost is returned when a cost is empty, negative, has more than
// two decimal places, or does not fit in int64 cents.
var ErrInvalidCost = errors.New("cost must be a non-negative amount with at most two decimals")

// ParseCents parses a dollar amount as cents. A leading "$" and thousands
// commas are ignored. One decimal place is tenths of a dollar ("0.5" is 50
// cents). A third decimal is rejected rather than rounded. A leading minus
// is rejected, including "-0.50", which is not zero dollars.
func ParseCents(raw string) (int64, error) {
	raw = strings.TrimSpace(strings.TrimPrefix(strings.ReplaceAll(raw, ",", ""), "$"))
	parts := strings.Split(raw, ".")
	if len(parts) > 2 || len(parts[0]) == 0 || strings.HasPrefix(parts[0], "-") {
		return 0, ErrInvalidCost
	}
	if len(parts) == 2 && len(parts[1]) > 2 {
		return 0, ErrInvalidCost
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole < 0 {
		return 0, ErrInvalidCost
	}
	frac := int64(0)
	if len(parts) == 2 {
		file := parts[1]
		if len(file) == 1 {
			file += "0"
		}
		if file != "" {
			frac, err = strconv.ParseInt(file, 10, 64)
			if err != nil {
				return 0, ErrInvalidCost
			}
		}
	}
	if whole > (math.MaxInt64-frac)/100 {
		return 0, ErrInvalidCost
	}
	return whole*100 + frac, nil
}

// AddCents adds two cent amounts. Sums that would overflow int64 saturate at
// the limits instead of wrapping. Service costs are non-negative, and a
// non-negative overflow caps at math.MaxInt64.
func AddCents(a, b int64) int64 {
	if b > 0 {
		if a > math.MaxInt64-b {
			return math.MaxInt64
		}
	} else if b < 0 {
		if a < math.MinInt64-b {
			return math.MinInt64
		}
	}
	return a + b
}

// spendGroup is one service type's priced records. lifetime is the uncapped
// sum, so two types that both display as MaxInt64 still sort by the real total.
type spendGroup struct {
	spend    ServiceTypeSpend
	lifetime *big.Int
}

// AggregateSpend totals priced service records through today. It is the
// database-free form of the vehicle-page rules. The page itself still asks
// Postgres for one row per service type, so a load does not read the history
// into this function.
//
// A nil cost is left out. Zero is a real cost and is counted. Year-to-date
// is January 1 through today in today's calendar, compared as dates. Each
// record keeps its own calendar date, so it is not shifted into today's time
// zone. Each service type's displayed total is capped with AddCents, then
// those type totals are added. Types are ordered by the uncapped lifetime
// sum, highest first, then by name, case-insensitively.
func AggregateSpend(records []Record, today time.Time) SpendSummary {
	year, _, _ := today.Date()
	start := dateOrdinal(time.Date(year, time.January, 1, 0, 0, 0, 0, today.Location()))
	end := dateOrdinal(today)
	groups := make(map[uuid.UUID]*spendGroup)
	var seen []uuid.UUID
	var costInt big.Int
	for i := range records {
		record := &records[i]
		if record.CostCents == nil {
			continue
		}
		group := groups[record.ServiceTypeID]
		if group == nil {
			group = &spendGroup{spend: ServiceTypeSpend{Name: record.ServiceTypeName}, lifetime: big.NewInt(0)}
			groups[record.ServiceTypeID] = group
			seen = append(seen, record.ServiceTypeID)
		}
		cost := *record.CostCents
		costInt.SetInt64(cost)
		group.lifetime.Add(group.lifetime, &costInt)
		group.spend.Records++
		group.spend.LifetimeCents = AddCents(group.spend.LifetimeCents, cost)
		if on := dateOrdinal(record.OccurredOn); on >= start && on <= end {
			group.spend.YearToDateCents = AddCents(group.spend.YearToDateCents, cost)
		}
	}
	if len(seen) == 0 {
		return SpendSummary{}
	}
	ordered := make([]*spendGroup, 0, len(seen))
	for _, id := range seen {
		ordered = append(ordered, groups[id])
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		left, right := ordered[i], ordered[j]
		if cmp := left.lifetime.Cmp(right.lifetime); cmp != 0 {
			return cmp > 0
		}
		leftName, rightName := strings.ToLower(left.spend.Name), strings.ToLower(right.spend.Name)
		if leftName != rightName {
			return leftName < rightName
		}
		return left.spend.Name < right.spend.Name
	})
	summary := SpendSummary{Types: make([]ServiceTypeSpend, 0, len(ordered))}
	for _, group := range ordered {
		summary.Types = append(summary.Types, group.spend)
		summary.PricedRecords += group.spend.Records
		summary.YearToDateCents = AddCents(summary.YearToDateCents, group.spend.YearToDateCents)
		summary.LifetimeCents = AddCents(summary.LifetimeCents, group.spend.LifetimeCents)
	}
	return summary
}

// dateOrdinal is the calendar date in t's own location, packed so later dates
// compare greater. Month and day are zero-padded in the decimal form.
func dateOrdinal(t time.Time) int64 {
	year, month, day := t.Date()
	return int64(year)*10000 + int64(month)*100 + int64(day)
}
