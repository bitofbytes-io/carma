// Package calendar renders reminder due dates as an iCalendar (RFC 5545) feed
// that calendar apps can subscribe to.
package calendar

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bitofbytes-io/carma/internal/reminder"
)

// RefreshInterval is how often subscribed calendar apps are asked to refetch
// the feed. Some apps, Google Calendar among them, refresh on their own
// schedule instead.
const RefreshInterval = "PT6H"

// Feed returns an iCalendar document with an all-day event on the due date of
// each enabled reminder that has a time interval. Mileage-only reminders are
// left out because their dates are only estimates. now stamps the events;
// baseURL, when set, is the Carma origin that events link back to.
func Feed(results []reminder.Result, now time.Time, baseURL string) []byte {
	var b strings.Builder
	line := func(s string) { writeFolded(&b, s) }
	line("BEGIN:VCALENDAR")
	line("VERSION:2.0")
	line("PRODID:-//bitofbytes//Carma//EN")
	line("CALSCALE:GREGORIAN")
	line("METHOD:PUBLISH")
	line("X-WR-CALNAME:Carma reminders")
	line("X-WR-CALDESC:Vehicle service due dates from Carma")
	line("REFRESH-INTERVAL;VALUE=DURATION:" + RefreshInterval)
	line("X-PUBLISHED-TTL:" + RefreshInterval)
	stamp := now.UTC().Format("20060102T150405Z")
	for _, result := range results {
		r := result.Reminder
		if !r.Enabled || result.DueDate == nil {
			continue
		}
		due := *result.DueDate
		line("BEGIN:VEVENT")
		line("UID:reminder-" + r.ID.String() + "@carma")
		line("DTSTAMP:" + stamp)
		line("DTSTART;VALUE=DATE:" + due.Format("20060102"))
		line("DTEND;VALUE=DATE:" + due.AddDate(0, 0, 1).Format("20060102"))
		line("SUMMARY:" + escape(r.ServiceTypeName+" due · "+r.VehicleName))
		line("DESCRIPTION:" + escape(description(result)))
		if baseURL != "" {
			line("URL:" + baseURL + "/vehicles/" + r.VehicleID.String() + "/reminders")
		}
		line("TRANSP:TRANSPARENT")
		line("END:VEVENT")
	}
	line("END:VCALENDAR")
	return []byte(b.String())
}

func description(result reminder.Result) string {
	r := result.Reminder
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
	text := r.ServiceTypeName + " for " + r.VehicleName + ", every " + strings.Join(intervals, " or ") + "."
	if result.Detail != "" {
		text += "\n" + result.Detail
	}
	return text
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
