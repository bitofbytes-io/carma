package reminder

import (
	"errors"
	"testing"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
)

func TestResolveSnoozePresets(t *testing.T) {
	today := time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC)
	latest := int64(10000)
	twoWeeks, err := ResolveSnooze(SnoozeTwoWeeks, "", "", today, &latest)
	if err != nil || twoWeeks.UntilMiles != nil || twoWeeks.UntilOn == nil || !twoWeeks.UntilOn.Equal(time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("two weeks = %+v err=%v", twoWeeks, err)
	}
	oneMonth, err := ResolveSnooze("  "+SnoozeOneMonth+"  ", "", "", today, &latest)
	if err != nil || oneMonth.UntilOn == nil || !oneMonth.UntilOn.Equal(time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("one month = %+v err=%v", oneMonth, err)
	}
	midMonth := time.Date(2026, 3, 18, 0, 0, 0, 0, time.UTC)
	april, err := ResolveSnooze(SnoozeOneMonth, "", "", midMonth, &latest)
	if err != nil || april.UntilOn == nil || !april.UntilOn.Equal(time.Date(2026, 4, 18, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("mid-month = %+v err=%v", april, err)
	}
	jan31 := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	february, err := ResolveSnooze(SnoozeOneMonth, "", "", jan31, &latest)
	if err != nil || february.UntilOn == nil || !february.UntilOn.Equal(time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("jan 31 = %+v err=%v", february, err)
	}
	mar31 := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	aprilEnd, err := ResolveSnooze(SnoozeOneMonth, "", "", mar31, &latest)
	if err != nil || aprilEnd.UntilOn == nil || !aprilEnd.UntilOn.Equal(time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("mar 31 = %+v err=%v", aprilEnd, err)
	}
	leap := time.Date(2024, 1, 31, 0, 0, 0, 0, time.UTC)
	leapDay, err := ResolveSnooze(SnoozeOneMonth, "", "", leap, &latest)
	if err != nil || leapDay.UntilOn == nil || !leapDay.UntilOn.Equal(time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("leap day = %+v err=%v", leapDay, err)
	}
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	evening := time.Date(2026, 1, 31, 22, 30, 0, 0, newYork)
	clamped, err := ResolveSnooze(SnoozeOneMonth, "", "", evening, &latest)
	if err != nil || clamped.UntilOn == nil || !clamped.UntilOn.Equal(time.Date(2026, 2, 28, 22, 30, 0, 0, newYork)) {
		t.Fatalf("jan 31 evening = %+v err=%v", clamped, err)
	}
	plus, err := ResolveSnooze(SnoozePlus500Miles, "", "", today, &latest)
	if err != nil || plus.UntilOn != nil || plus.UntilMiles == nil || *plus.UntilMiles != 10500 {
		t.Fatalf("+500 = %+v err=%v", plus, err)
	}
	if _, err = ResolveSnooze(SnoozePlus500Miles, "", "", today, nil); !errors.Is(err, ErrSnoozeMileageUnknown) {
		t.Fatalf("+500 without mileage err=%v", err)
	}
	cleared, err := ResolveSnooze(SnoozeClear, "2026-12-01", "99999", today, &latest)
	if err != nil || cleared.UntilOn != nil || cleared.UntilMiles != nil {
		t.Fatalf("clear = %+v err=%v", cleared, err)
	}
	if _, err = ResolveSnooze("later", "", "", today, &latest); !errors.Is(err, ErrSnoozePreset) {
		t.Fatalf("unknown preset err=%v", err)
	}
}

func TestResolveCustomSnooze(t *testing.T) {
	today := time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC)
	latest := int64(10000)
	both, err := ResolveSnooze(SnoozeCustom, "2026-11-13", "15500", today, &latest)
	if err != nil || both.UntilOn == nil || !both.UntilOn.Equal(time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)) || both.UntilMiles == nil || *both.UntilMiles != 15500 {
		t.Fatalf("custom = %+v err=%v", both, err)
	}
	dateOnly, err := ResolveSnooze(SnoozeCustom, "2026-08-01", "", today, nil)
	if err != nil || dateOnly.UntilOn == nil || dateOnly.UntilMiles != nil {
		t.Fatalf("date only = %+v err=%v", dateOnly, err)
	}
	for _, tc := range []struct {
		name, date, miles string
		latest            *int64
		want              error
	}{
		{name: "today", date: "2026-07-31", want: ErrSnoozeDate},
		{name: "past", date: "2026-07-30", want: ErrSnoozeDate},
		{name: "garbage date", date: "next friday", want: ErrSnoozeDate},
		{name: "garbage miles", miles: "soon", latest: &latest, want: ErrSnoozeMiles},
		{name: "zero miles", miles: "0", latest: &latest, want: ErrSnoozeMiles},
		{name: "miles without a reading", miles: "12000", want: ErrSnoozeMileageUnknown},
		{name: "miles not above the reading", miles: "10000", latest: &latest, want: ErrSnoozeMilesLow},
		{name: "empty", want: ErrSnoozeEmpty},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ResolveSnooze(SnoozeCustom, tc.date, tc.miles, today, tc.latest); !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want %v", err, tc.want)
			}
		})
	}
}

func TestEvaluateSnoozeDefersDueAndDueSoon(t *testing.T) {
	today := time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC)
	tomorrow := today.AddDate(0, 0, 1)
	overdue := model.Reminder{Enabled: true, IntervalMonths: ip(6), CreatedAt: today.AddDate(0, -6, 0), SnoozedUntilOn: &tomorrow}
	got := Evaluate(overdue, today)
	if got.Status != Snoozed || got.Detail != "snoozed until Aug 1, 2026" {
		t.Fatalf("date snooze = %s %q", got.Status, got.Detail)
	}
	overdue.SnoozedUntilOn = &today
	if got = Evaluate(overdue, today); got.Status != Due {
		t.Fatalf("snooze ending today = %s, want overdue", got.Status)
	}

	soon := model.Reminder{Enabled: true, IntervalMonths: ip(6), CreatedAt: today.AddDate(0, -6, 20), SnoozedUntilOn: &tomorrow}
	unsnoozed := soon
	unsnoozed.SnoozedUntilOn = nil
	if got = Evaluate(unsnoozed, today); got.Status != Soon {
		t.Fatalf("reminder before snooze = %s, want due soon", got.Status)
	}
	if got = Evaluate(soon, today); got.Status != Snoozed {
		t.Fatalf("due soon snooze = %s", got.Status)
	}

	miles := int64(10500)
	byMiles := model.Reminder{
		Enabled: true, IntervalMiles: i64p(500), StartingOdometer: i64p(10000), LatestOdometer: i64p(10500),
		SnoozedUntilMiles: &miles,
	}
	if got = Evaluate(byMiles, today); got.Status != Due {
		t.Fatalf("mileage already reached = %s", got.Status)
	}
	miles = 11000
	if got = Evaluate(byMiles, today); got.Status != Snoozed || got.Detail != "snoozed until 11,000 mi" {
		t.Fatalf("mileage snooze = %s %q", got.Status, got.Detail)
	}
	byMiles.LatestOdometer = nil
	if got = Evaluate(byMiles, today); got.Status != Snoozed {
		t.Fatalf("unknown mileage does not end a mileage snooze: %s", got.Status)
	}

	bothMiles := int64(15500)
	both := overdue
	both.SnoozedUntilOn = &tomorrow
	both.SnoozedUntilMiles = &bothMiles
	both.LatestOdometer = i64p(10000)
	if got = Evaluate(both, today); got.Status != Snoozed || got.Detail != "snoozed until Aug 1, 2026 and 15,500 mi" {
		t.Fatalf("both = %s %q", got.Status, got.Detail)
	}
	both.LatestOdometer = &bothMiles
	if got = Evaluate(both, today); got.Detail != "snoozed until Aug 1, 2026" {
		t.Fatalf("date still active = %q", got.Detail)
	}
	both.SnoozedUntilOn = &today
	both.LatestOdometer = i64p(10000)
	if got = Evaluate(both, today); got.Detail != "snoozed until 15,500 mi" {
		t.Fatalf("mileage still active = %q", got.Detail)
	}

	disabled := overdue
	disabled.Enabled = false
	disabled.SnoozedUntilOn = &tomorrow
	if got = Evaluate(disabled, today); got.Status != Disabled {
		t.Fatalf("disabled snooze = %s", got.Status)
	}

	quiet := model.Reminder{Enabled: true, IntervalMonths: ip(6), CreatedAt: today, SnoozedUntilOn: &tomorrow}
	if got = Evaluate(quiet, today); got.Status != Snoozed {
		t.Fatalf("snooze before the interval is due = %s", got.Status)
	}
	quiet.SnoozedUntilOn = &today
	if got = Evaluate(quiet, today); got.Status != OK {
		t.Fatalf("expired snooze on a current reminder = %s", got.Status)
	}
}

func TestSnoozeUsesCalendarDate(t *testing.T) {
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	// A DATE column comes back as midnight UTC. The household date decides
	// whether that calendar day has arrived.
	until := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	r := model.Reminder{Enabled: true, IntervalMonths: ip(1), CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), SnoozedUntilOn: &until}
	if got := Evaluate(r, time.Date(2026, 7, 31, 0, 0, 0, 0, newYork)); got.Status != Snoozed || got.Detail != "snoozed until Aug 1, 2026" {
		t.Fatalf("evening before = %s %q", got.Status, got.Detail)
	}
	if got := Evaluate(r, time.Date(2026, 8, 1, 0, 0, 0, 0, newYork)); got.Status != Due {
		t.Fatalf("snooze date in New York = %s, want overdue", got.Status)
	}
}
