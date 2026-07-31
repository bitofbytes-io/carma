package reminder

import (
	"github.com/bitofbytes-io/carma/internal/model"
	"time"
)

type Status string

const (
	OK       Status = "ok"
	Soon     Status = "due-soon"
	Due      Status = "overdue"
	Disabled Status = "disabled"
)

type Result struct {
	Reminder   model.Reminder
	Status     Status
	Detail     string
	DueDate    *time.Time
	DueMileage *int64
}

func Evaluate(r model.Reminder, today time.Time) Result {
	res := Result{Reminder: r, Status: OK}
	if !r.Enabled {
		res.Status = Disabled
		return res
	}
	if r.Baseline == nil {
		res.Status = Due
		res.Detail = "No history yet"
		return res
	}
	due, soon := false, false
	if r.IntervalMonths != nil {
		d := r.Baseline.OccurredOn.AddDate(0, *r.IntervalMonths, 0)
		res.DueDate = &d
		if !today.Before(d) {
			due = true
		} else if !today.Before(d.AddDate(0, 0, -30)) {
			soon = true
		}
	}
	if r.IntervalMiles != nil && r.Baseline.OdometerMiles != nil {
		d := *r.Baseline.OdometerMiles + *r.IntervalMiles
		res.DueMileage = &d
		if r.LatestOdometer != nil {
			if *r.LatestOdometer >= d {
				due = true
			} else if d-*r.LatestOdometer <= 500 {
				soon = true
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
