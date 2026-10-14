package reminder

import (
	"math"
	"strconv"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
)

type Status string

const (
	OK       Status = "ok"
	Soon     Status = "due-soon"
	Due      Status = "overdue"
	Disabled Status = "disabled"
)

const (
	soonDays  = 30
	soonMiles = 500
	// EstimateWindowMonths is how far back mileage history feeds the estimate.
	EstimateWindowMonths = 12
	// minEstimateSpanDays is the shortest stretch of history worth fitting.
	minEstimateSpanDays = 14
	// maxProjectionDays caps projections too far out to be meaningful.
	maxProjectionDays = 10 * 365
)

type Result struct {
	Reminder   model.Reminder
	Status     Status
	Detail     string
	DueDate    *time.Time
	DueMileage *int64
	// MilesLeft is set while a known mileage interval has not been reached.
	MilesLeft *int64
	// ProjectedDueDate estimates when MilesLeft runs out at the vehicle's
	// MilesPerDay.
	ProjectedDueDate *time.Time
}

// Today returns the current calendar date at midnight in loc, the time zone that
// due dates are evaluated in. A nil loc means UTC.
func Today(now time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	year, month, date := now.In(loc).Date()
	return time.Date(year, month, date, 0, 0, 0, 0, loc)
}

// Evaluate classifies r on the calendar date today (see Today). Baseline dates
// are interpreted as calendar dates in today's time zone.
func Evaluate(r model.Reminder, today time.Time) Result {
	loc := today.Location()
	res := Result{Reminder: r, Status: OK}
	if !r.Enabled {
		res.Status = Disabled
		return res
	}
	due, soon := false, false
	if r.IntervalMonths != nil {
		year, month, date := r.CreatedAt.In(loc).Date()
		if r.Baseline != nil {
			year, month, date = r.Baseline.OccurredOn.Date()
		}
		baselineDate := time.Date(year, month, date, 0, 0, 0, 0, loc)
		d := baselineDate.AddDate(0, *r.IntervalMonths, 0)
		res.DueDate = &d
		if !today.Before(d) {
			due = true
		} else if !today.Before(d.AddDate(0, 0, -soonDays)) {
			soon = true
		}
	}
	if r.IntervalMiles != nil {
		baselineOdometer := r.StartingOdometer
		if r.Baseline != nil {
			baselineOdometer = r.Baseline.OdometerMiles
		}
		if baselineOdometer == nil {
			if due {
				res.Status = Due
			} else if soon {
				res.Status = Soon
			}
			return res
		}
		d := *baselineOdometer + *r.IntervalMiles
		res.DueMileage = &d
		if r.LatestOdometer != nil {
			if *r.LatestOdometer >= d {
				due = true
			} else {
				left := d - *r.LatestOdometer
				res.MilesLeft = &left
				if left <= soonMiles {
					soon = true
				}
				res.Detail = "~" + groupThousands(left) + " mi left"
				if r.MilesPerDay == nil || !(*r.MilesPerDay > 0) {
					res.Detail += " · add a mileage reading to estimate"
				} else if days := float64(left) / *r.MilesPerDay; days <= maxProjectionDays {
					projected := today.AddDate(0, 0, int(math.Round(days)))
					res.ProjectedDueDate = &projected
					res.Detail += ", est. " + shortDate(projected, today)
					// A projection only brings a reminder forward to due soon;
					// it is overdue once a reading actually reaches the interval.
					if !projected.After(today.AddDate(0, 0, soonDays)) {
						soon = true
					}
				}
			}
		}
	}
	if due {
		res.Status = Due
	} else if soon {
		res.Status = Soon
	}
	return res
}

// EstimateMilesPerDay fits a least-squares line through the points dated in
// the EstimateWindowMonths up to today and returns its slope. It returns nil
// when fewer than two points span at least minEstimateSpanDays, or when the
// fitted mileage does not increase.
func EstimateMilesPerDay(points []model.MileagePoint, today time.Time) *float64 {
	from, to := dayNumber(today.AddDate(0, -EstimateWindowMonths, 0)), dayNumber(today)
	var xs, ys []float64
	first, last := to, from
	for _, point := range points {
		day := dayNumber(point.On)
		if day < from || day > to {
			continue
		}
		first, last = min(first, day), max(last, day)
		xs = append(xs, float64(day-from))
		ys = append(ys, float64(point.Miles))
	}
	if len(xs) < 2 || last-first < minEstimateSpanDays {
		return nil
	}
	var meanX, meanY float64
	for i := range xs {
		meanX += xs[i]
		meanY += ys[i]
	}
	meanX /= float64(len(xs))
	meanY /= float64(len(ys))
	var covariance, variance float64
	for i := range xs {
		covariance += (xs[i] - meanX) * (ys[i] - meanY)
		variance += (xs[i] - meanX) * (xs[i] - meanX)
	}
	slope := covariance / variance
	if !(slope > 0) || math.IsInf(slope, 0) {
		return nil
	}
	return &slope
}

// dayNumber counts calendar days since the Unix epoch for t's date in its own
// location, so stored DATE values and local "today" compare by date.
func dayNumber(t time.Time) int64 {
	year, month, date := t.Date()
	return time.Date(year, month, date, 0, 0, 0, 0, time.UTC).Unix() / 86400
}

// shortDate formats a projected date, adding the year once it is more than
// about six months away.
func shortDate(t, today time.Time) string {
	if t.After(today.AddDate(0, 6, 0)) {
		return t.Format("Jan 2, 2006")
	}
	return t.Format("Jan 2")
}

// groupThousands formats a nonnegative count with comma separators.
func groupThousands(n int64) string {
	digits := strconv.FormatInt(n, 10)
	for i := len(digits) - 3; i > 0; i -= 3 {
		digits = digits[:i] + "," + digits[i:]
	}
	return digits
}
