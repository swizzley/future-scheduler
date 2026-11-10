// Package schedule holds the booking rules. It is pure: no I/O, no database,
// and "now" is always passed in, so every rule is testable on its own.
package schedule

import (
	"errors"
	"fmt"
	"time"
)

const (
	// SlotLength is the fixed length of every appointment.
	SlotLength = 30 * time.Minute
	// MaxRange caps an availability query so one request can't ask for years of slots.
	MaxRange = 31 * 24 * time.Hour

	openMinute  = 8 * 60  // 8:00am
	closeMinute = 17 * 60 // 5:00pm; the last slot starts at 4:30pm
)

// Pacific is the business time zone. Rules are evaluated in wall-clock
// Pacific time, so "8am-5pm" holds across daylight saving changes.
var Pacific = mustLoad("America/Los_Angeles")

func mustLoad(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(fmt.Sprintf("load time zone %s: %v", name, err))
	}
	return loc
}

// Interval is a half-open time range [Start, End).
type Interval struct {
	Start, End time.Time
}

// Overlaps reports whether two half-open intervals share any time.
// Back-to-back appointments (one ends at 10:00, the next starts at 10:00) do not overlap.
func (a Interval) Overlaps(b Interval) bool {
	return a.Start.Before(b.End) && b.Start.Before(a.End)
}

// ValidationError is a rule violation the client can fix; the API returns it as a 400.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(format string, args ...any) error {
	return &ValidationError{Msg: fmt.Sprintf(format, args...)}
}

// IsValidation reports whether err is a client-fixable rule violation.
func IsValidation(err error) bool {
	var v *ValidationError
	return errors.As(err, &v)
}

// InBusinessHours reports whether a slot starting at start falls entirely
// inside Monday-Friday, 8am-5pm Pacific.
func InBusinessHours(start time.Time) bool {
	local := start.In(Pacific)
	if wd := local.Weekday(); wd == time.Saturday || wd == time.Sunday {
		return false
	}
	minute := local.Hour()*60 + local.Minute()
	return minute >= openMinute && minute+int(SlotLength/time.Minute) <= closeMinute
}

// ValidateBooking checks a new appointment against every rule except
// overlap, which the database enforces so that concurrent bookings can't race.
func ValidateBooking(start, end, now time.Time) error {
	if !end.After(start) {
		return invalid("ends_at must be after starts_at")
	}
	if end.Sub(start) != SlotLength {
		return invalid("appointments must be exactly 30 minutes (got %s)", end.Sub(start))
	}
	local := start.In(Pacific)
	if (local.Minute() != 0 && local.Minute() != 30) || local.Second() != 0 || local.Nanosecond() != 0 {
		return invalid("appointments must start on the hour or half hour")
	}
	if !InBusinessHours(start) {
		return invalid("appointments must be Monday-Friday, 8am-5pm Pacific")
	}
	if start.Before(now) {
		return invalid("appointments can't be booked in the past")
	}
	return nil
}

// ValidateRange checks an availability query window.
func ValidateRange(from, to time.Time) error {
	if !to.After(from) {
		return invalid("ends_at must be after starts_at")
	}
	if to.Sub(from) > MaxRange {
		return invalid("range can't be longer than 31 days")
	}
	return nil
}

// AvailableSlots returns every bookable 30-minute slot that lies entirely
// within [from, to), is inside business hours, is not in the past, and does
// not overlap any booked interval. Slots are returned in order.
func AvailableSlots(from, to, now time.Time, booked []Interval) []Interval {
	// Align to the next :00 or :30. Pacific offsets are whole hours, so
	// aligning the absolute time to 30 minutes aligns the wall clock too.
	start := from.Truncate(SlotLength)
	if start.Before(from) {
		start = start.Add(SlotLength)
	}

	var slots []Interval
	for s := start; !s.Add(SlotLength).After(to); s = s.Add(SlotLength) {
		slot := Interval{Start: s, End: s.Add(SlotLength)}
		if s.Before(now) || !InBusinessHours(s) || overlapsAny(slot, booked) {
			continue
		}
		slots = append(slots, slot)
	}
	return slots
}

func overlapsAny(slot Interval, booked []Interval) bool {
	for _, b := range booked {
		if slot.Overlaps(b) {
			return true
		}
	}
	return false
}
