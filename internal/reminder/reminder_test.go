package reminder

import (
	"github.com/bitofbytes-io/carma/internal/model"
	"testing"
	"time"
)

func ip(v int) *int       { return &v }
func i64p(v int64) *int64 { return &v }
func TestEvaluateSemantics(t *testing.T) {
	today := time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC)
	base := model.Record{OccurredOn: time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC), OdometerMiles: i64p(10000)}
	r := model.Reminder{Enabled: true, IntervalMonths: ip(6), IntervalMiles: i64p(5000), Baseline: &base, LatestOdometer: i64p(12000)}
	if got := Evaluate(r, today).Status; got != Due {
		t.Fatalf("7 month baseline=%s", got)
	}
	base.OccurredOn = today
	base.OdometerMiles = i64p(12000)
	if got := Evaluate(r, today).Status; got != OK {
		t.Fatalf("new baseline=%s", got)
	}
}
func TestEvaluateNoHistoryAndMileageUnknown(t *testing.T) {
	today := time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC)
	r := model.Reminder{Enabled: true, IntervalMiles: i64p(5000)}
	if got := Evaluate(r, today).Status; got != Due {
		t.Fatal(got)
	}
	base := model.Record{OccurredOn: today}
	r.Baseline = &base
	r.LatestOdometer = i64p(999999)
	if got := Evaluate(r, today).Status; got != OK {
		t.Fatalf("mileage without baseline should be unevaluable: %s", got)
	}
}
func TestDueOverridesSoonAndCalendarMonths(t *testing.T) {
	today := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	base := model.Record{OccurredOn: time.Date(2024, 1, 31, 0, 0, 0, 0, time.UTC), OdometerMiles: i64p(1000)}
	r := model.Reminder{Enabled: true, IntervalMonths: ip(1), IntervalMiles: i64p(5000), Baseline: &base, LatestOdometer: i64p(6000)}
	got := Evaluate(r, today)
	if got.Status != Due {
		t.Fatalf("got %s due date %v", got.Status, got.DueDate)
	}
	if !got.DueDate.Equal(time.Date(2024, 3, 2, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("expected Go AddDate calendar behavior, got %v", got.DueDate)
	}
}
