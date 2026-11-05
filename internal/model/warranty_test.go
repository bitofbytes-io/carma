package model

import (
	"errors"
	"testing"
	"time"
)

func TestWarrantyActive(t *testing.T) {
	day := func(y int, m time.Month, d int) *time.Time {
		date := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
		return &date
	}
	miles := func(n int64) *int64 { return &n }
	today := time.Date(2026, time.June, 15, 0, 0, 0, 0, time.FixedZone("EDT", -4*3600))

	for _, tc := range []struct {
		name     string
		until    *time.Time
		limit    *int64
		odometer *int64
		want     bool
	}{
		{name: "no warranty", odometer: miles(1000)},
		{name: "date in the future", until: day(2027, time.January, 1), want: true},
		{name: "date is today", until: day(2026, time.June, 15), want: true},
		{name: "date passed yesterday", until: day(2026, time.June, 14)},
		{name: "date long past", until: day(2020, time.January, 1)},
		{name: "mileage below limit", limit: miles(36000), odometer: miles(35000), want: true},
		{name: "mileage at limit", limit: miles(36000), odometer: miles(36000), want: true},
		{name: "mileage past limit", limit: miles(36000), odometer: miles(36001)},
		{name: "mileage limit without odometer", limit: miles(36000), want: true},
		{name: "zero mileage limit at zero", limit: miles(0), odometer: miles(0), want: true},
		{name: "both open", until: day(2027, time.January, 1), limit: miles(36000), odometer: miles(10000), want: true},
		{name: "date open mileage passed", until: day(2027, time.January, 1), limit: miles(36000), odometer: miles(40000)},
		{name: "date passed mileage open", until: day(2026, time.January, 1), limit: miles(36000), odometer: miles(10000)},
		{name: "both passed", until: day(2026, time.January, 1), limit: miles(36000), odometer: miles(40000)},
		{name: "date only ignores odometer", until: day(2027, time.January, 1), odometer: miles(900000), want: true},
		{name: "mileage only ignores date", limit: miles(36000), odometer: miles(10000), want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := Record{WarrantyUntilOn: tc.until, WarrantyUntilMiles: tc.limit}
			if got := record.WarrantyActive(today, tc.odometer); got != tc.want {
				t.Fatalf("WarrantyActive=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestWarrantyActiveComparesCalendarDatesNotInstants(t *testing.T) {
	until := time.Date(2026, time.June, 15, 0, 0, 0, 0, time.UTC)
	record := Record{WarrantyUntilOn: &until}
	lateInDay := time.Date(2026, time.June, 15, 23, 59, 0, 0, time.FixedZone("HST", -10*3600))
	if !record.WarrantyActive(lateInDay, nil) {
		t.Fatal("warranty ended before the end of its last day")
	}
	nextDay := time.Date(2026, time.June, 16, 0, 0, 0, 0, time.FixedZone("HST", -10*3600))
	if record.WarrantyActive(nextDay, nil) {
		t.Fatal("warranty still active the day after its date")
	}
}

func TestWarrantyLimits(t *testing.T) {
	until := time.Date(2028, time.March, 3, 0, 0, 0, 0, time.UTC)
	miles, small := int64(36000), int64(500)
	for _, tc := range []struct {
		name   string
		record Record
		want   string
		has    bool
	}{
		{name: "neither"},
		{name: "date", record: Record{WarrantyUntilOn: &until}, want: "Mar 3, 2028", has: true},
		{name: "miles", record: Record{WarrantyUntilMiles: &miles}, want: "36,000 mi", has: true},
		{name: "small miles", record: Record{WarrantyUntilMiles: &small}, want: "500 mi", has: true},
		{name: "both", record: Record{WarrantyUntilOn: &until, WarrantyUntilMiles: &miles}, want: "Mar 3, 2028 or 36,000 mi", has: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.record.WarrantyLimits(); got != tc.want {
				t.Fatalf("WarrantyLimits=%q want=%q", got, tc.want)
			}
			if got := tc.record.HasWarranty(); got != tc.has {
				t.Fatalf("HasWarranty=%v want=%v", got, tc.has)
			}
		})
	}
}

func TestValidateWarranty(t *testing.T) {
	service := time.Date(2026, time.June, 15, 0, 0, 0, 0, time.UTC)
	before, same, after := service.AddDate(0, 0, -1), service, service.AddDate(1, 0, 0)
	if err := ValidateWarranty(service, nil); err != nil {
		t.Fatalf("nil warranty: %v", err)
	}
	if err := ValidateWarranty(service, &after); err != nil {
		t.Fatalf("later warranty: %v", err)
	}
	if err := ValidateWarranty(service, &same); err != nil {
		t.Fatalf("same-day warranty: %v", err)
	}
	if err := ValidateWarranty(service, &before); !errors.Is(err, ErrWarrantyBeforeService) {
		t.Fatalf("earlier warranty err=%v", err)
	}
}
