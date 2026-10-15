package reminder

import (
	"math"
	"strconv"
	"time"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/google/uuid"
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
					hint := r.EstimateHint
					if hint == "" {
						hint = noHistoryHint
					}
					res.Detail += " · " + hint
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

// ProjectionVehicles returns the vehicles with enabled mileage reminders, whose
// mileage history EvaluateAll uses to project due dates.
func ProjectionVehicles(reminders []model.Reminder) []uuid.UUID {
	var vehicleIDs []uuid.UUID
	seen := map[uuid.UUID]bool{}
	for _, schedule := range reminders {
		if schedule.Enabled && schedule.IntervalMiles != nil && !seen[schedule.VehicleID] {
			seen[schedule.VehicleID] = true
			vehicleIDs = append(vehicleIDs, schedule.VehicleID)
		}
	}
	return vehicleIDs
}

// EvaluateAll evaluates reminders on today, projecting mileage intervals from
// each vehicle's mileage points in the EstimateWindowMonths before today.
func EvaluateAll(reminders []model.Reminder, points []model.MileagePoint, today time.Time) []Result {
	byVehicle := map[uuid.UUID][]model.MileagePoint{}
	for _, point := range points {
		byVehicle[point.VehicleID] = append(byVehicle[point.VehicleID], point)
	}
	rates := map[uuid.UUID]*float64{}
	hints := map[uuid.UUID]string{}
	for _, id := range ProjectionVehicles(reminders) {
		rates[id] = EstimateMilesPerDay(byVehicle[id], today)
		hints[id] = EstimateHint(byVehicle[id], today)
	}
	results := make([]Result, 0, len(reminders))
	for _, schedule := range reminders {
		schedule.MilesPerDay = rates[schedule.VehicleID]
		schedule.EstimateHint = hints[schedule.VehicleID]
		results = append(results, Evaluate(schedule, today))
	}
	return results
}

// noHistoryHint explains what a vehicle with no recent mileage needs before its
// due date can be estimated.
var noHistoryHint = "log mileage on two dates " + strconv.Itoa(minEstimateSpanDays) + "+ days apart to estimate a date"

// EstimateHint says what mileage history EstimateMilesPerDay still needs to
// find a driving rate from points, or "" when it already has one.
func EstimateHint(points []model.MileagePoint, today time.Time) string {
	xs, _, first, last := windowPoints(points, today)
	switch {
	case len(xs) == 0:
		return noHistoryHint
	case len(xs) < 2 || last-first < minEstimateSpanDays:
		// A reading on or after this date spans enough days from the first.
		from := today.AddDate(0, -EstimateWindowMonths, 0)
		next := time.Date(from.Year(), from.Month(), from.Day()+int(first)+minEstimateSpanDays, 0, 0, 0, 0, today.Location())
		if !next.After(today) {
			return "log mileage again to estimate a date"
		}
		return "log mileage again on or after " + shortDate(next, today) + " to estimate a date"
	case EstimateMilesPerDay(points, today) == nil:
		return "recent mileage isn't increasing, so no date estimate"
	}
	return ""
}

// EstimateMilesPerDay fits a least-squares line through the points dated in
// the EstimateWindowMonths up to today and returns its slope. It returns nil
// when fewer than two points span at least minEstimateSpanDays, or when the
// fitted mileage does not increase.
func EstimateMilesPerDay(points []model.MileagePoint, today time.Time) *float64 {
	xs, ys, first, last := windowPoints(points, today)
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

// windowPoints returns the points dated in the EstimateWindowMonths up to
// today as days into that window and miles, with the first and last day seen.
func windowPoints(points []model.MileagePoint, today time.Time) (xs, ys []float64, first, last int64) {
	from, to := dayNumber(today.AddDate(0, -EstimateWindowMonths, 0)), dayNumber(today)
	first, last = to-from, 0
	for _, point := range points {
		day := dayNumber(point.On)
		if day < from || day > to {
			continue
		}
		first, last = min(first, day-from), max(last, day-from)
		xs = append(xs, float64(day-from))
		ys = append(ys, float64(point.Miles))
	}
	return xs, ys, first, last
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
