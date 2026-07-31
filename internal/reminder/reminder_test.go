package reminder

import (
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
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
