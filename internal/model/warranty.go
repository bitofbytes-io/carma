package model

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// HasWarranty reports whether the record has a warranty date, a mileage
// limit, or both.
func (r Record) HasWarranty() bool {
	return r.WarrantyUntilOn != nil || r.WarrantyUntilMiles != nil
}

// WarrantyActive reports whether the record's warranty still covers the
// vehicle on the calendar date today at the given odometer. A warranty ends at
// whichever set limit passes first: the date is covered through its own day,
// and the mileage through the limit itself. A mileage limit cannot be judged
// without an odometer, so it does not count as passed while odometer is nil.
// A record with no warranty is never active.
func (r Record) WarrantyActive(today time.Time, odometer *int64) bool {
	if !r.HasWarranty() {
		return false
	}
	if r.WarrantyUntilOn != nil && dateAfter(today, *r.WarrantyUntilOn) {
		return false
	}
	if r.WarrantyUntilMiles != nil && odometer != nil && *odometer > *r.WarrantyUntilMiles {
		return false
	}
	return true
}

// dateAfter reports whether the calendar date of day is later than that of
// limit. Both are compared by year, month, and day so a time of day, or a
// different location, never moves the cutoff.
func dateAfter(day, limit time.Time) bool {
	dy, dm, dd := day.Date()
	ly, lm, ld := limit.Date()
	if dy != ly {
		return dy > ly
	}
	if dm != lm {
		return dm > lm
	}
	return dd > ld
}

// WarrantyLimits describes the warranty limits in plain words, such as
// "Mar 3, 2028 or 36,000 mi". It is empty when the record has no warranty.
func (r Record) WarrantyLimits() string {
	var parts []string
	if r.WarrantyUntilOn != nil {
		parts = append(parts, r.WarrantyUntilOn.Format("Jan 2, 2006"))
	}
	if r.WarrantyUntilMiles != nil {
		parts = append(parts, groupDigits(*r.WarrantyUntilMiles)+" mi")
	}
	return strings.Join(parts, " or ")
}

func groupDigits(n int64) string {
	digits := fmt.Sprintf("%d", n)
	for i := len(digits) - 3; i > 0; i -= 3 {
		digits = digits[:i] + "," + digits[i:]
	}
	return digits
}

// ErrWarrantyBeforeService is returned when a warranty ends before the work.
var ErrWarrantyBeforeService = errors.New("warranty date cannot be before the service date")

// ValidateWarranty checks the warranty limits against the service date: a
// warranty cannot end before the work was done. Mileage is validated when it
// is parsed.
func ValidateWarranty(occurredOn time.Time, until *time.Time) error {
	if until != nil && until.Before(occurredOn) {
		return ErrWarrantyBeforeService
	}
	return nil
}
