package model

import (
	"errors"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
)

func cents(n int64) *int64 { return &n }

func TestParseCents(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    int64
		wantErr bool
	}{
		{name: "zero dollars", raw: "0", want: 0},
		{name: "zero with cents", raw: "0.00", want: 0},
		{name: "zero with one decimal", raw: "0.0", want: 0},
		{name: "whole dollars", raw: "12", want: 1200},
		{name: "dollar sign", raw: "$89.00", want: 8900},
		{name: "dollar sign and space", raw: "$ 89", want: 8900},
		{name: "thousands commas", raw: "1,234.50", want: 123450},
		{name: "one decimal is tenths of a dollar", raw: "0.5", want: 50},
		{name: "one decimal pads without rounding", raw: "12.3", want: 1230},
		{name: "two decimals", raw: "12.34", want: 1234},
		{name: "trailing decimal point is zero cents", raw: "1.", want: 100},
		{name: "leading zeros", raw: "00.50", want: 50},
		{name: "explicit plus", raw: "+1.5", want: 150},
		{name: "max int64 cents", raw: "92233720368547758.07", want: math.MaxInt64},
		{name: "empty", raw: "", wantErr: true},
		{name: "dollar only", raw: "$", wantErr: true},
		{name: "words", raw: "x", wantErr: true},
		{name: "negative", raw: "-1", wantErr: true},
		{name: "negative zero", raw: "-0", wantErr: true},
		{name: "negative zero dollars with cents", raw: "-0.50", wantErr: true},
		{name: "negative tenths", raw: "-0.5", wantErr: true},
		{name: "third decimal is not rounded", raw: "1.234", wantErr: true},
		{name: "third decimal half is not rounded", raw: "1.235", wantErr: true},
		{name: "two dots", raw: "1.2.3", wantErr: true},
		{name: "leading dot", raw: ".50", wantErr: true},
		{name: "one cent past max", raw: "92233720368547758.08", wantErr: true},
		{name: "whole dollars past max", raw: "92233720368547759", wantErr: true},
		{name: "does not fit", raw: "999999999999999999", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseCents(test.raw)
			if test.wantErr {
				if !errors.Is(err, ErrInvalidCost) || got != 0 {
					t.Fatalf("ParseCents(%q)=%d,%v want 0, ErrInvalidCost", test.raw, got, err)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("ParseCents(%q)=%d,%v want %d", test.raw, got, err, test.want)
			}
		})
	}
}

func TestAddCents(t *testing.T) {
	tests := []struct {
		name string
		a, b int64
		want int64
	}{
		{name: "both zero", a: 0, b: 0, want: 0},
		{name: "add to zero", a: 0, b: 5, want: 5},
		{name: "add zero", a: 5, b: 0, want: 5},
		{name: "ordinary sum", a: 2, b: 3, want: 5},
		{name: "max plus zero", a: math.MaxInt64, b: 0, want: math.MaxInt64},
		{name: "zero plus max", a: 0, b: math.MaxInt64, want: math.MaxInt64},
		{name: "one below max plus one", a: math.MaxInt64 - 1, b: 1, want: math.MaxInt64},
		{name: "one below max plus two", a: math.MaxInt64 - 1, b: 2, want: math.MaxInt64},
		{name: "max plus one", a: math.MaxInt64, b: 1, want: math.MaxInt64},
		{name: "one plus max", a: 1, b: math.MaxInt64, want: math.MaxInt64},
		{name: "half plus half plus leftover", a: math.MaxInt64 / 2, b: math.MaxInt64/2 + 1, want: math.MaxInt64},
		{name: "negative second argument", a: 5, b: -3, want: 2},
		{name: "negative first argument", a: -1, b: 5, want: 4},
		{name: "negative first plus max", a: -1, b: math.MaxInt64, want: math.MaxInt64 - 1},
		{name: "two negatives", a: -40, b: -2, want: -42},
		{name: "min plus negative one", a: math.MinInt64, b: -1, want: math.MinInt64},
		{name: "negative one plus min", a: -1, b: math.MinInt64, want: math.MinInt64},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := AddCents(test.a, test.b); got != test.want {
				t.Fatalf("AddCents(%d, %d)=%d want %d", test.a, test.b, got, test.want)
			}
		})
	}
}

func TestAggregateSpendEmptyAndMissingCosts(t *testing.T) {
	today := time.Date(2026, time.July, 31, 0, 0, 0, 0, time.UTC)
	oil := uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	for _, records := range [][]Record{
		nil,
		{},
		{{ServiceTypeID: oil, ServiceTypeName: "Oil", OccurredOn: today, CostCents: nil}},
	} {
		summary := AggregateSpend(records, today)
		if summary.YearToDateCents != 0 || summary.LifetimeCents != 0 || summary.PricedRecords != 0 || summary.Types != nil {
			t.Fatalf("unpriced summary=%+v", summary)
		}
	}
}

func TestAggregateSpendZeroAndNegative(t *testing.T) {
	today := time.Date(2026, time.July, 31, 0, 0, 0, 0, time.UTC)
	oil := uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	tires := uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	summary := AggregateSpend([]Record{
		{ServiceTypeID: oil, ServiceTypeName: "Oil", OccurredOn: today, CostCents: cents(0)},
		{ServiceTypeID: oil, ServiceTypeName: "Oil", OccurredOn: today, CostCents: nil},
		{ServiceTypeID: tires, ServiceTypeName: "Tires", OccurredOn: today, CostCents: cents(-150)},
		{ServiceTypeID: tires, ServiceTypeName: "Tires", OccurredOn: today, CostCents: cents(40)},
	}, today)
	if summary.YearToDateCents != -110 || summary.LifetimeCents != -110 || summary.PricedRecords != 3 {
		t.Fatalf("totals=%+v", summary)
	}
	if len(summary.Types) != 2 {
		t.Fatalf("types=%+v", summary.Types)
	}
	// A zero total sorts ahead of a negative one.
	oilSpend, tireSpend := summary.Types[0], summary.Types[1]
	if oilSpend.Name != "Oil" || oilSpend.Records != 1 || oilSpend.YearToDateCents != 0 || oilSpend.LifetimeCents != 0 {
		t.Fatalf("oil=%+v", oilSpend)
	}
	if tireSpend.Name != "Tires" || tireSpend.Records != 2 || tireSpend.YearToDateCents != -110 || tireSpend.LifetimeCents != -110 {
		t.Fatalf("tires=%+v", tireSpend)
	}
}

func TestAggregateSpendYearBoundaries(t *testing.T) {
	oil := uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	edt := time.FixedZone("EDT", -4*3600)
	// Late in the household's day. The clock time must not drop today's record.
	today := time.Date(2026, time.July, 31, 23, 59, 0, 0, edt)
	summary := AggregateSpend([]Record{
		{ServiceTypeID: oil, ServiceTypeName: "Oil", OccurredOn: time.Date(2025, time.December, 31, 0, 0, 0, 0, time.UTC), CostCents: cents(1)},
		{ServiceTypeID: oil, ServiceTypeName: "Oil", OccurredOn: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC), CostCents: cents(2)},
		{ServiceTypeID: oil, ServiceTypeName: "Oil", OccurredOn: time.Date(2026, time.July, 31, 0, 0, 0, 0, time.UTC), CostCents: cents(4)},
		{ServiceTypeID: oil, ServiceTypeName: "Oil", OccurredOn: time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC), CostCents: cents(8)},
		{ServiceTypeID: oil, ServiceTypeName: "Oil", OccurredOn: time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC), CostCents: cents(16)},
		{ServiceTypeID: oil, ServiceTypeName: "Oil", OccurredOn: time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC)},
	}, today)
	// January 1 and July 31 count this year. December 31, August 1, and next
	// year count only toward lifetime. The missing cost is left out.
	if summary.YearToDateCents != 6 || summary.LifetimeCents != 31 || summary.PricedRecords != 5 {
		t.Fatalf("window totals=%+v", summary)
	}
	if len(summary.Types) != 1 || summary.Types[0].Records != 5 || summary.Types[0].YearToDateCents != 6 {
		t.Fatalf("type=%+v", summary.Types)
	}

	// A record stored as the evening before January 1 in its own zone is still
	// December 31, even though that instant is already January 1 in UTC.
	est := time.FixedZone("EST", -5*3600)
	newYear := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
	kept := AggregateSpend([]Record{{
		ServiceTypeID:   oil,
		ServiceTypeName: "Oil",
		OccurredOn:      time.Date(2025, time.December, 31, 23, 0, 0, 0, est),
		CostCents:       cents(9),
	}}, newYear)
	if kept.YearToDateCents != 0 || kept.LifetimeCents != 9 {
		t.Fatalf("zone-local date totals=%+v", kept)
	}

	// Household today can still be December 31 after UTC has rolled the year.
	pst := time.FixedZone("PST", -8*3600)
	yearEnd := time.Date(2025, time.December, 31, 21, 0, 0, 0, pst)
	rolled := AggregateSpend([]Record{
		{ServiceTypeID: oil, ServiceTypeName: "Oil", OccurredOn: time.Date(2025, time.December, 31, 0, 0, 0, 0, time.UTC), CostCents: cents(3)},
		{ServiceTypeID: oil, ServiceTypeName: "Oil", OccurredOn: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC), CostCents: cents(5)},
	}, yearEnd)
	if rolled.YearToDateCents != 3 || rolled.LifetimeCents != 8 {
		t.Fatalf("year-end household totals=%+v", rolled)
	}

	jan1 := time.Date(2026, time.January, 1, 0, 30, 0, 0, time.FixedZone("CET", 3600))
	opened := AggregateSpend([]Record{
		{ServiceTypeID: oil, ServiceTypeName: "Oil", OccurredOn: time.Date(2025, time.December, 31, 0, 0, 0, 0, time.UTC), CostCents: cents(7)},
		{ServiceTypeID: oil, ServiceTypeName: "Oil", OccurredOn: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC), CostCents: cents(11)},
	}, jan1)
	if opened.YearToDateCents != 11 || opened.LifetimeCents != 18 {
		t.Fatalf("new year totals=%+v", opened)
	}
}

func TestAggregateSpendOrdersTypesAndCapsOverflow(t *testing.T) {
	oil := uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	tires := uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	brakes := uuid.MustParse("cccccccc-cccc-cccc-cccc-cccccccccccc")
	pads := uuid.MustParse("dddddddd-dddd-dddd-dddd-dddddddddddd")
	today := time.Date(2026, time.July, 31, 0, 0, 0, 0, time.UTC)
	lastYear := time.Date(2025, time.June, 1, 0, 0, 0, 0, time.UTC)
	summary := AggregateSpend([]Record{
		{ServiceTypeID: oil, ServiceTypeName: "Oil", OccurredOn: today, CostCents: cents(100)},
		{ServiceTypeID: tires, ServiceTypeName: "Tires", OccurredOn: lastYear, CostCents: cents(500)},
		{ServiceTypeID: brakes, ServiceTypeName: "brakes", OccurredOn: today, CostCents: cents(500)},
		{ServiceTypeID: pads, ServiceTypeName: "Brake pads", OccurredOn: today, CostCents: cents(500)},
	}, today)
	if summary.YearToDateCents != 1100 || summary.LifetimeCents != 1600 || summary.PricedRecords != 4 {
		t.Fatalf("totals=%+v", summary)
	}
	got := make([]string, len(summary.Types))
	for i, group := range summary.Types {
		got[i] = group.Name + ":" + strconv.FormatInt(group.LifetimeCents, 10)
	}
	// Equal lifetime totals break ties by case-insensitive name. "Brake pads"
	// and "brakes" share a lifetime with "Tires", and last year's tires add
	// nothing to the year-to-date column.
	want := []string{"Brake pads:500", "brakes:500", "Tires:500", "Oil:100"}
	if len(got) != len(want) {
		t.Fatalf("types=%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("types=%v want %v", got, want)
		}
	}
	if summary.Types[2].YearToDateCents != 0 || summary.Types[2].Records != 1 {
		t.Fatalf("prior-year type=%+v", summary.Types[2])
	}

	// Both displayed totals saturate. "Aaa" would win a name sort, and the
	// larger uncapped sum must still come first.
	huge := AggregateSpend([]Record{
		{ServiceTypeID: oil, ServiceTypeName: "Aaa", OccurredOn: today, CostCents: cents(math.MaxInt64)},
		{ServiceTypeID: oil, ServiceTypeName: "Aaa", OccurredOn: lastYear, CostCents: cents(50)},
		{ServiceTypeID: tires, ServiceTypeName: "Wash", OccurredOn: today, CostCents: cents(math.MaxInt64)},
		{ServiceTypeID: tires, ServiceTypeName: "Wash", OccurredOn: today, CostCents: cents(math.MaxInt64)},
	}, today)
	if huge.YearToDateCents != math.MaxInt64 || huge.LifetimeCents != math.MaxInt64 || huge.PricedRecords != 4 {
		t.Fatalf("capped totals=%+v", huge)
	}
	if len(huge.Types) != 2 || huge.Types[0].Name != "Wash" || huge.Types[1].Name != "Aaa" {
		t.Fatalf("saturated types=%+v, want the larger uncapped sum first", huge.Types)
	}
	if huge.Types[0].LifetimeCents != math.MaxInt64 || huge.Types[0].YearToDateCents != math.MaxInt64 || huge.Types[0].Records != 2 {
		t.Fatalf("wash=%+v", huge.Types[0])
	}
	if huge.Types[1].LifetimeCents != math.MaxInt64 || huge.Types[1].YearToDateCents != math.MaxInt64 || huge.Types[1].Records != 2 {
		t.Fatalf("aaa=%+v", huge.Types[1])
	}
}

func TestAggregateSpendGroupsByServiceTypeID(t *testing.T) {
	today := time.Date(2026, time.July, 31, 0, 0, 0, 0, time.UTC)
	first := uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	second := uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	summary := AggregateSpend([]Record{
		{ServiceTypeID: first, ServiceTypeName: "Other", OccurredOn: today, CostCents: cents(10)},
		{ServiceTypeID: second, ServiceTypeName: "Other", OccurredOn: today, CostCents: cents(20)},
	}, today)
	if summary.PricedRecords != 2 || summary.LifetimeCents != 30 || len(summary.Types) != 2 {
		t.Fatalf("same name, different types=%+v", summary)
	}
	if summary.Types[0].Records != 1 || summary.Types[0].LifetimeCents != 20 || summary.Types[1].LifetimeCents != 10 {
		t.Fatalf("types=%+v", summary.Types)
	}
}
