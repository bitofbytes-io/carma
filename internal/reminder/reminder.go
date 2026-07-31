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
	due, soon := false, false
	if r.IntervalMonths != nil {
		year, month, date := r.CreatedAt.Date()
		baselineDate := time.Date(year, month, date, 0, 0, 0, 0, r.CreatedAt.Location())
		if r.Baseline != nil {
			baselineDate = r.Baseline.OccurredOn
		}
		d := baselineDate.AddDate(0, *r.IntervalMonths, 0)
		res.DueDate = &d
		if !today.Before(d) {
			due = true
		} else if !today.Before(d.AddDate(0, 0, -30)) {
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
