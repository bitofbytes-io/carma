// Package calendar renders reminder due dates as an iCalendar (RFC 5545) feed
// that calendar apps can subscribe to.
package calendar

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/reminder"
)

// RefreshInterval is how often subscribed calendar apps are asked to refetch
// the feed. Some apps, Google Calendar among them, refresh on their own
// schedule instead.
const RefreshInterval = "PT6H"

// Feed returns an iCalendar document with an all-day event on the due date of
// each enabled reminder that has a time interval. Mileage-only reminders are
// left out because their dates are only estimates. baseURL, when set, is the
// Carma origin that events link back to.
func Feed(results []reminder.Result, baseURL string) []byte {
	var b strings.Builder
	line := func(s string) { writeFolded(&b, s) }
	line("BEGIN:VCALENDAR")
	line("VERSION:2.0")
	line("PRODID:-//bitofbytes//Carma//EN")
	line("CALSCALE:GREGORIAN")
	line("X-WR-CALNAME:Carma reminders")
	line("X-WR-CALDESC:Vehicle service due dates from Carma")
	line("REFRESH-INTERVAL;VALUE=DURATION:" + RefreshInterval)
	line("X-PUBLISHED-TTL:" + RefreshInterval)
	for _, result := range results {
		r := result.Reminder
		if !r.Enabled || result.DueDate == nil {
			continue
		}
		due := *result.DueDate
		line("BEGIN:VEVENT")
		line("UID:reminder-" + r.ID.String() + "@carma")
		line("DTSTAMP:" + revised(r).UTC().Format("20060102T150405Z"))
		line("DTSTART;VALUE=DATE:" + due.Format("20060102"))
		line("DTEND;VALUE=DATE:" + due.AddDate(0, 0, 1).Format("20060102"))
		line("SUMMARY:" + escape(r.ServiceTypeName+" due · "+r.VehicleName))
		line("DESCRIPTION:" + escape(description(r)))
		if baseURL != "" {
			line("URL:" + baseURL + "/vehicles/" + r.VehicleID.String() + "/reminders")
		}
		line("TRANSP:TRANSPARENT")
		line("END:VEVENT")
	}
	line("END:VCALENDAR")
	return []byte(b.String())
}

func description(r model.Reminder) string {
	var intervals []string
	if r.IntervalMonths != nil {
		unit := " months"
		if *r.IntervalMonths == 1 {
			unit = " month"
		}
		intervals = append(intervals, strconv.Itoa(*r.IntervalMonths)+unit)
	}
	if r.IntervalMiles != nil {
		intervals = append(intervals, strconv.FormatInt(*r.IntervalMiles, 10)+" miles")
	}
	return r.ServiceTypeName + " for " + r.VehicleName + ", every " + strings.Join(intervals, " or ") + "."
}

// revised is when r's due date last changed in Carma: when the reminder was
// saved or its baseline service record was logged or edited. A feed without a
// METHOD uses it as each event's DTSTAMP (RFC 5545 section 3.8.7.2), so
// unchanged events keep their stamp from one fetch to the next.
func revised(r model.Reminder) time.Time {
	times := []time.Time{r.CreatedAt, r.UpdatedAt}
	if r.Baseline != nil {
		times = append(times, r.Baseline.CreatedAt, r.Baseline.UpdatedAt)
	}
	latest := times[0]
	for _, t := range times[1:] {
		if t.After(latest) {
			latest = t
		}
	}
	return latest
}

// escape escapes an iCalendar TEXT value.
func escape(s string) string {
	return strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\r\n", `\n`, "\n", `\n`, "\r", `\n`).Replace(s)
}

// writeFolded writes a content line ending in CRLF, folding it so no line is
// longer than 75 octets and no UTF-8 character is split.
func writeFolded(b *strings.Builder, s string) {
	limit := 75
	for len(s) > limit {
		cut := limit
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		b.WriteString(s[:cut])
		b.WriteString("\r\n ")
		s = s[cut:]
		// Continuation lines start with a space, which counts toward the limit.
		limit = 74
	}
	b.WriteString(s)
	b.WriteString("\r\n")
}
