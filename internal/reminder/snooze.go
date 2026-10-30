package reminder

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
)

const (
	// SnoozeTwoWeeks, SnoozeOneMonth, and SnoozePlus500Miles are the preset
	// values posted from the reminders page. SnoozeCustom reads the date and
	// mileage fields. SnoozeClear removes both thresholds.
	SnoozeTwoWeeks     = "2w"
	SnoozeOneMonth     = "1m"
	SnoozePlus500Miles = "500mi"
	SnoozeCustom       = "custom"
	SnoozeClear        = "clear"
	// SnoozeMileStep is how far SnoozePlus500Miles defers the current reading.
	SnoozeMileStep = 500
)

var (
	ErrSnoozeMileageUnknown = errors.New("snooze mileage unknown")
	ErrSnoozeDate           = errors.New("snooze date")
	ErrSnoozeMiles          = errors.New("snooze miles")
	ErrSnoozeMilesLow       = errors.New("snooze miles low")
	ErrSnoozeEmpty          = errors.New("snooze empty")
	ErrSnoozePreset         = errors.New("snooze preset")
)

// Snooze is a deferral to store on a reminder. A nil field clears that threshold.
type Snooze struct {
	UntilOn    *time.Time
	UntilMiles *int64
}

// ResolveSnooze turns a reminders-page choice into thresholds to store.
// today is the household calendar date (see Today). latest is the vehicle's
// current mileage and is required for a mileage snooze. SnoozeClear returns
// an empty Snooze, which clears both thresholds. A preset replaces any
// previous snooze rather than adding to it.
func ResolveSnooze(preset, untilOn, untilMiles string, today time.Time, latest *int64) (Snooze, error) {
	switch strings.TrimSpace(preset) {
	case SnoozeTwoWeeks:
		date := today.AddDate(0, 0, 14)
		return Snooze{UntilOn: &date}, nil
	case SnoozeOneMonth:
		date := today.AddDate(0, 1, 0)
		return Snooze{UntilOn: &date}, nil
	case SnoozePlus500Miles:
		if latest == nil {
			return Snooze{}, ErrSnoozeMileageUnknown
		}
		miles := *latest + SnoozeMileStep
		return Snooze{UntilMiles: &miles}, nil
	case SnoozeClear:
		return Snooze{}, nil
	case SnoozeCustom:
		return customSnooze(untilOn, untilMiles, today, latest)
	default:
		return Snooze{}, ErrSnoozePreset
	}
}

func customSnooze(untilOn, untilMiles string, today time.Time, latest *int64) (Snooze, error) {
	var snooze Snooze
	untilOn = strings.TrimSpace(untilOn)
	untilMiles = strings.TrimSpace(untilMiles)
	if untilOn != "" {
		parsed, err := time.Parse("2006-01-02", untilOn)
		if err != nil {
			return Snooze{}, ErrSnoozeDate
		}
		date := time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 0, 0, 0, 0, today.Location())
		if !date.After(today) {
			return Snooze{}, ErrSnoozeDate
		}
		snooze.UntilOn = &date
	}
	if untilMiles != "" {
		miles, err := strconv.ParseInt(untilMiles, 10, 64)
		if err != nil || miles <= 0 {
			return Snooze{}, ErrSnoozeMiles
		}
		if latest == nil {
			return Snooze{}, ErrSnoozeMileageUnknown
		}
		if miles <= *latest {
			return Snooze{}, ErrSnoozeMilesLow
		}
		snooze.UntilMiles = &miles
	}
	if snooze.UntilOn == nil && snooze.UntilMiles == nil {
		return Snooze{}, ErrSnoozeEmpty
	}
	return snooze, nil
}

// activeSnooze reports whether r is still deferred on today, and the phrase
// that describes it ("snoozed until …"). A date passes on that calendar day.
// A mileage passes once the latest reading reaches it; an unknown mileage has
// not passed. With both set, the reminder stays deferred until each has passed.
func activeSnooze(r model.Reminder, today time.Time) (string, bool) {
	var parts []string
	if r.SnoozedUntilOn != nil {
		year, month, day := r.SnoozedUntilOn.Date()
		until := time.Date(year, month, day, 0, 0, 0, 0, today.Location())
		if today.Before(until) {
			parts = append(parts, until.Format("Jan 2, 2006"))
		}
	}
	if r.SnoozedUntilMiles != nil && (r.LatestOdometer == nil || *r.LatestOdometer < *r.SnoozedUntilMiles) {
		parts = append(parts, groupThousands(*r.SnoozedUntilMiles)+" mi")
	}
	if len(parts) == 0 {
		return "", false
	}
	return "snoozed until " + strings.Join(parts, " and "), true
}
