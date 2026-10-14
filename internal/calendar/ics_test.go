package calendar

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/reminder"
	"github.com/google/uuid"
)

func TestFeedListsDatedEnabledRemindersAsAllDayEvents(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")
	months, miles := 6, int64(5000)
	due := time.Date(2027, 1, 31, 0, 0, 0, 0, loc)
	id, vehicleID := uuid.New(), uuid.New()
	results := []reminder.Result{
		{Reminder: model.Reminder{ID: id, VehicleID: vehicleID, VehicleName: "Mom's car; the blue one", ServiceTypeName: "Oil, filter", IntervalMonths: &months, IntervalMiles: &miles, Enabled: true}, DueDate: &due, Detail: "~1,200 mi left"},
		{Reminder: model.Reminder{ID: uuid.New(), VehicleName: "Truck", ServiceTypeName: "Tires", IntervalMiles: &miles, Enabled: true}},
		{Reminder: model.Reminder{ID: uuid.New(), VehicleName: "Van", ServiceTypeName: "Wipers", IntervalMonths: &months}, DueDate: &due},
	}
	feed := string(Feed(results, time.Date(2026, 10, 14, 23, 5, 9, 0, loc), "https://carma.example"))
	unfolded := strings.ReplaceAll(feed, "\r\n ", "")
	for _, want := range []string{
		"BEGIN:VCALENDAR\r\nVERSION:2.0\r\n",
		"BEGIN:VEVENT\r\nUID:reminder-" + id.String() + "@carma\r\nDTSTAMP:20261015T030509Z\r\nDTSTART;VALUE=DATE:20270131\r\nDTEND;VALUE=DATE:20270201\r\n",
		`SUMMARY:Oil\, filter due · Mom's car\; the blue one` + "\r\n",
		`DESCRIPTION:Oil\, filter for Mom's car\; the blue one\, every 6 months or 5000 miles.\n~1\,200 mi left` + "\r\n",
		"URL:https://carma.example/vehicles/" + vehicleID.String() + "/reminders\r\n",
		"END:VCALENDAR\r\n",
	} {
		if !strings.Contains(unfolded, want) {
			t.Fatalf("feed missing %q:\n%s", want, feed)
		}
	}
	if strings.Count(feed, "BEGIN:VEVENT") != 1 {
		t.Fatalf("want only the enabled reminder with a due date:\n%s", feed)
	}
	if strings.Contains(strings.ReplaceAll(feed, "\r\n", ""), "\n") {
		t.Fatal("feed has a bare LF")
	}
}

func TestFeedWithoutBaseURLOmitsLinks(t *testing.T) {
	months := 1
	due := time.Date(2027, 1, 31, 0, 0, 0, 0, time.UTC)
	feed := string(Feed([]reminder.Result{{Reminder: model.Reminder{ID: uuid.New(), ServiceTypeName: "Wash", VehicleName: "Car", IntervalMonths: &months, Enabled: true}, DueDate: &due}}, due, ""))
	if strings.Contains(feed, "URL:") || !strings.Contains(feed, "every 1 month.") {
		t.Fatalf("unexpected feed:\n%s", feed)
	}
}

func TestWriteFoldedKeepsLinesShortAndCharactersWhole(t *testing.T) {
	value := "SUMMARY:" + strings.Repeat("é·x", 60)
	var b strings.Builder
	writeFolded(&b, value)
	lines := strings.Split(strings.TrimSuffix(b.String(), "\r\n"), "\r\n")
	if len(lines) < 3 {
		t.Fatalf("expected folding, got %d lines", len(lines))
	}
	for i, line := range lines {
		if len(line) > 75 || !utf8.ValidString(line) || (i > 0 && !strings.HasPrefix(line, " ")) {
			t.Fatalf("bad line %d (%d octets): %q", i, len(line), line)
		}
	}
	if got := strings.ReplaceAll(strings.TrimSuffix(b.String(), "\r\n"), "\r\n ", ""); got != value {
		t.Fatalf("unfolded %q, want %q", got, value)
	}
}
