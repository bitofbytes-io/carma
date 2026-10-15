package reminder

import (
	"math"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/google/uuid"
)

func ip(v int) *int       { return &v }
func i64p(v int64) *int64 { return &v }

func TestEvaluateFirstCycleUsesReminderBaselines(t *testing.T) {
	today := time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC)
	r := model.Reminder{
		Enabled:          true,
		IntervalMonths:   ip(6),
		IntervalMiles:    i64p(5000),
		CreatedAt:        today.AddDate(0, 0, -14),
		StartingOdometer: i64p(1200),
		LatestOdometer:   i64p(1250),
	}
	got := Evaluate(r, today)
	if got.Status != OK {
		t.Fatalf("new reminder without history = %s, want ok", got.Status)
	}
	if got.DueDate == nil || !got.DueDate.Equal(r.CreatedAt.AddDate(0, 6, 0)) {
		t.Fatalf("due date = %v", got.DueDate)
	}
	if got.DueMileage == nil || *got.DueMileage != 6200 {
		t.Fatalf("due mileage = %v", got.DueMileage)
	}
}

func TestEvaluateDimensionsAndBoundaries(t *testing.T) {
	today := time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		r    model.Reminder
		want Status
	}{
		{name: "time due on boundary", r: model.Reminder{Enabled: true, IntervalMonths: ip(6), CreatedAt: today.AddDate(0, -6, 0)}, want: Due},
		{name: "creation time does not delay boundary", r: model.Reminder{Enabled: true, IntervalMonths: ip(6), CreatedAt: time.Date(2026, 1, 31, 23, 59, 0, 0, time.UTC)}, want: Due},
		{name: "time soon on boundary", r: model.Reminder{Enabled: true, IntervalMonths: ip(5), CreatedAt: time.Date(2026, 3, 30, 0, 0, 0, 0, time.UTC)}, want: Soon},
		{name: "mileage due on boundary", r: model.Reminder{Enabled: true, IntervalMiles: i64p(5000), StartingOdometer: i64p(1000), LatestOdometer: i64p(6000)}, want: Due},
		{name: "mileage soon on boundary", r: model.Reminder{Enabled: true, IntervalMiles: i64p(5000), StartingOdometer: i64p(1000), LatestOdometer: i64p(5500)}, want: Soon},
		{name: "missing starting mileage is neutral", r: model.Reminder{Enabled: true, IntervalMiles: i64p(5000), LatestOdometer: i64p(999999)}, want: OK},
		{name: "missing mileage still evaluates time", r: model.Reminder{Enabled: true, IntervalMonths: ip(6), IntervalMiles: i64p(5000), CreatedAt: today.AddDate(0, -6, 0), LatestOdometer: i64p(999999)}, want: Due},
		{name: "disabled", r: model.Reminder{Enabled: false, IntervalMonths: ip(1), CreatedAt: today.AddDate(-1, 0, 0)}, want: Disabled},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Evaluate(tc.r, today).Status; got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

func TestMatchingRecordOverridesFirstCycleBaselines(t *testing.T) {
	today := time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC)
	base := model.Record{OccurredOn: today, OdometerMiles: i64p(12000)}
	r := model.Reminder{
		Enabled:          true,
		IntervalMonths:   ip(6),
		IntervalMiles:    i64p(5000),
		CreatedAt:        today.AddDate(-2, 0, 0),
		StartingOdometer: i64p(1000),
		Baseline:         &base,
		LatestOdometer:   i64p(12000),
	}
	if got := Evaluate(r, today).Status; got != OK {
		t.Fatalf("matching record did not reset old first-cycle baselines: %s", got)
	}

	base.OccurredOn = time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)
	if got := Evaluate(r, today).Status; got != Due {
		t.Fatalf("old matching record = %s, want overdue", got)
	}
}

func TestCalendarMonthBehavior(t *testing.T) {
	today := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	base := model.Record{OccurredOn: time.Date(2024, 1, 31, 0, 0, 0, 0, time.UTC)}
	r := model.Reminder{Enabled: true, IntervalMonths: ip(1), Baseline: &base}
	got := Evaluate(r, today)
	if got.Status != Soon || got.DueDate == nil || !got.DueDate.Equal(time.Date(2024, 3, 2, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("status=%s due date=%v", got.Status, got.DueDate)
	}
}

func f64p(v float64) *float64 { return &v }

func TestEstimateMilesPerDay(t *testing.T) {
	today := time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC)
	point := func(daysAgo int, miles int64) model.MileagePoint {
		return model.MileagePoint{On: today.AddDate(0, 0, -daysAgo), Miles: miles}
	}
	tests := []struct {
		name   string
		points []model.MileagePoint
		want   *float64
	}{
		{name: "no points"},
		{name: "one point", points: []model.MileagePoint{point(30, 1000)}},
		{name: "span under two weeks", points: []model.MileagePoint{point(13, 1000), point(0, 1500)}},
		{name: "two points two weeks apart", points: []model.MileagePoint{point(14, 1000), point(0, 1420)}, want: f64p(30)},
		{name: "linear fit", points: []model.MileagePoint{point(100, 1000), point(50, 1600), point(0, 2000)}, want: f64p(10)},
		{name: "uneven points", points: []model.MileagePoint{point(60, 10000), point(45, 13000), point(0, 14000)}, want: f64p(110000.0 / 1950)},
		{name: "older than twelve months ignored", points: []model.MileagePoint{point(400, 0), point(100, 1000), point(0, 2000)}, want: f64p(10)},
		{name: "future dates ignored", points: []model.MileagePoint{point(-30, 99999), point(100, 1000), point(0, 2000)}, want: f64p(10)},
		{name: "only old points", points: []model.MileagePoint{point(500, 0), point(400, 1000)}},
		{name: "decreasing mileage", points: []model.MileagePoint{point(60, 2000), point(0, 1000)}},
		{name: "flat mileage", points: []model.MileagePoint{point(60, 1000), point(0, 1000)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := EstimateMilesPerDay(tc.points, today)
			if (got == nil) != (tc.want == nil) || (got != nil && math.Abs(*got-*tc.want) > 1e-9) {
				t.Fatalf("got %v want %v", deref(got), deref(tc.want))
			}
		})
	}
}

func TestEstimateHint(t *testing.T) {
	today := time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC)
	point := func(daysAgo int, miles int64) model.MileagePoint {
		return model.MileagePoint{On: today.AddDate(0, 0, -daysAgo), Miles: miles}
	}
	tests := []struct {
		name   string
		points []model.MileagePoint
		want   string
	}{
		{name: "no points", want: "log mileage on two dates 14+ days apart to estimate a date"},
		{name: "only old points", points: []model.MileagePoint{point(500, 0), point(400, 1000)}, want: "log mileage on two dates 14+ days apart to estimate a date"},
		{name: "one point today", points: []model.MileagePoint{point(0, 8000)}, want: "log mileage again on or after Aug 14 to estimate a date"},
		{name: "span under two weeks", points: []model.MileagePoint{point(10, 1000), point(0, 1500)}, want: "log mileage again on or after Aug 4 to estimate a date"},
		{name: "one old enough point", points: []model.MileagePoint{point(30, 1000)}, want: "log mileage again to estimate a date"},
		{name: "flat mileage", points: []model.MileagePoint{point(60, 1000), point(0, 1000)}, want: "recent mileage isn't increasing, so no date estimate"},
		{name: "has a rate", points: []model.MileagePoint{point(14, 1000), point(0, 1420)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := EstimateHint(tc.points, today); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestEstimateHintDateIsFourteenDaysAfterFirstPoint(t *testing.T) {
	today := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		first time.Time
		want  string
	}{
		{first: today, want: "log mileage again on or after Oct 28 to estimate a date"},
		{first: today.AddDate(0, 0, -5), want: "log mileage again on or after Oct 23 to estimate a date"},
		{first: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), want: "log mileage again on or after Oct 15 to estimate a date"},
		{first: time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC), want: "log mileage again to estimate a date"},
	}
	for _, tc := range tests {
		points := []model.MileagePoint{{On: tc.first, Miles: 1000}}
		if got := EstimateHint(points, today); got != tc.want {
			t.Fatalf("first point %s: got %q want %q", tc.first.Format("Jan 2"), got, tc.want)
		}
		// A reading on the hinted date is enough for a rate.
		next := tc.first.AddDate(0, 0, minEstimateSpanDays)
		if EstimateMilesPerDay(append(points, model.MileagePoint{On: next, Miles: 1500}), next) == nil {
			t.Fatalf("first point %s: no rate with a reading on %s", tc.first.Format("Jan 2"), next.Format("Jan 2"))
		}
	}
}

func TestEvaluateAllUsesVehicleMileageHint(t *testing.T) {
	today := time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC)
	vehicleID := uuid.New()
	r := model.Reminder{VehicleID: vehicleID, Enabled: true, IntervalMiles: i64p(10000), StartingOdometer: i64p(1102), LatestOdometer: i64p(2204)}
	// The vehicle's current mileage, set today, is its only point.
	points := []model.MileagePoint{{VehicleID: vehicleID, On: today, Miles: 2204}}
	got := EvaluateAll([]model.Reminder{r}, points, today)[0]
	if want := "~8,898 mi left · log mileage again on or after Aug 14 to estimate a date"; got.Detail != want {
		t.Fatalf("detail=%q want %q", got.Detail, want)
	}
	// A later reading gives a driving rate of 30 mi a day.
	points = append(points, model.MileagePoint{VehicleID: vehicleID, On: today.AddDate(0, 0, 20), Miles: 2804})
	got = EvaluateAll([]model.Reminder{{VehicleID: vehicleID, Enabled: true, IntervalMiles: i64p(10000), StartingOdometer: i64p(1102), LatestOdometer: i64p(2804)}}, points, today.AddDate(0, 0, 20))[0]
	if want := "~8,298 mi left, est. May 24, 2027"; got.Detail != want {
		t.Fatalf("detail=%q want %q", got.Detail, want)
	}
}

func deref(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func TestEvaluateProjectsMileageDueDate(t *testing.T) {
	today := time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC)
	mileage := func(latest int64, rate *float64) model.Reminder {
		return model.Reminder{Enabled: true, IntervalMiles: i64p(5000), StartingOdometer: i64p(10000), LatestOdometer: i64p(latest), MilesPerDay: rate}
	}
	tests := []struct {
		name       string
		r          model.Reminder
		want       Status
		projectIn  int
		wantDetail string
	}{
		{name: "far off", r: mileage(11900, f64p(30)), want: OK, projectIn: 103, wantDetail: "~3,100 mi left, est. Nov 11"},
		{name: "projection after thirty days", r: mileage(11900, f64p(100)), want: OK, projectIn: 31, wantDetail: "~3,100 mi left, est. Aug 31"},
		{name: "projection on thirty days is soon", r: mileage(12000, f64p(100)), want: Soon, projectIn: 30, wantDetail: "~3,000 mi left, est. Aug 30"},
		{name: "projection soon before mileage is", r: mileage(14000, f64p(50)), want: Soon, projectIn: 20, wantDetail: "~1,000 mi left, est. Aug 20"},
		{name: "mileage soon before projection is", r: mileage(14600, f64p(1)), want: Soon, projectIn: 400, wantDetail: "~400 mi left, est. Sep 4, 2027"},
		{name: "fractional days round", r: mileage(14000, f64p(110000.0/1950)), want: Soon, projectIn: 18, wantDetail: "~1,000 mi left, est. Aug 18"},
		{name: "projection never makes overdue", r: mileage(14990, f64p(1000)), want: Soon, projectIn: 0, wantDetail: "~10 mi left, est. Jul 31"},
		{name: "no estimate", r: mileage(14000, nil), want: OK, projectIn: -1, wantDetail: "~1,000 mi left · log mileage on two dates 14+ days apart to estimate a date"},
		{name: "zero rate", r: mileage(14000, f64p(0)), want: OK, projectIn: -1, wantDetail: "~1,000 mi left · log mileage on two dates 14+ days apart to estimate a date"},
		{name: "projection too far out", r: mileage(10000, f64p(.001)), want: OK, projectIn: -1, wantDetail: "~5,000 mi left"},
		{name: "overdue by mileage", r: mileage(15200, f64p(30)), want: Due, projectIn: -1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Evaluate(tc.r, today)
			if got.Status != tc.want || got.Detail != tc.wantDetail {
				t.Fatalf("status=%s detail=%q, want %s %q", got.Status, got.Detail, tc.want, tc.wantDetail)
			}
			if tc.projectIn < 0 {
				if got.ProjectedDueDate != nil {
					t.Fatalf("projected %v, want none", got.ProjectedDueDate)
				}
				return
			}
			if want := today.AddDate(0, 0, tc.projectIn); got.ProjectedDueDate == nil || !got.ProjectedDueDate.Equal(want) {
				t.Fatalf("projected %v, want %v", got.ProjectedDueDate, want)
			}
		})
	}
}

func TestProjectionDoesNotHideTimeStatus(t *testing.T) {
	today := time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC)
	r := model.Reminder{
		Enabled: true, IntervalMonths: ip(6), IntervalMiles: i64p(5000), CreatedAt: today.AddDate(0, -6, 0),
		StartingOdometer: i64p(10000), LatestOdometer: i64p(10100), MilesPerDay: f64p(10),
	}
	if got := Evaluate(r, today); got.Status != Due || got.MilesLeft == nil || *got.MilesLeft != 4900 {
		t.Fatalf("status=%s miles left=%v", got.Status, got.MilesLeft)
	}
}

func TestTodayUsesConfiguredTimeZone(t *testing.T) {
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	// 23:30 on July 30 in New York is already July 31 in UTC.
	now := time.Date(2026, 7, 30, 23, 30, 0, 0, newYork).UTC()
	today := Today(now, newYork)
	if y, m, d := today.Date(); y != 2026 || m != 7 || d != 30 || today.Location() != newYork {
		t.Fatalf("today = %v", today)
	}
	if got := Today(now, nil); !got.Equal(time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("nil location today = %v", got)
	}

	baseline := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC) // stored DATE
	r := model.Reminder{Enabled: true, IntervalMonths: ip(6), Baseline: &model.Record{OccurredOn: baseline}}
	if got := Evaluate(r, today); got.Status != Soon {
		t.Fatalf("due July 31 was %s at 23:30 on July 30 local", got.Status)
	}
	if got := Evaluate(r, Today(now.Add(time.Hour), newYork)); got.Status != Due {
		t.Fatalf("due July 31 was %s at 00:30 on July 31 local", got.Status)
	}
	if y, m, d := Evaluate(r, today).DueDate.Date(); y != 2026 || m != 7 || d != 31 {
		t.Fatalf("due date moved across time zones: %v", Evaluate(r, today).DueDate)
	}

	// A reminder created late in the local evening starts its cycle that day.
	created := model.Reminder{Enabled: true, IntervalMonths: ip(1), CreatedAt: now}
	if y, m, d := Evaluate(created, today).DueDate.Date(); y != 2026 || m != 8 || d != 30 {
		t.Fatalf("created-date due = %v", Evaluate(created, today).DueDate)
	}
}
