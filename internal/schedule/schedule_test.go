package schedule

import (
	"testing"
	"time"
)

func pt(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, Pacific)
	if err != nil {
		panic(err)
	}
	return t
}

var longAgo = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

func TestValidateBooking(t *testing.T) {
	cases := []struct {
		name    string
		start   string
		minutes int
		ok      bool
	}{
		{"weekday morning", "2019-01-24 09:00", 30, true},
		{"half hour start", "2019-01-24 09:30", 30, true},
		{"first slot 8am", "2019-01-24 08:00", 30, true},
		{"last slot 4:30pm", "2019-01-24 16:30", 30, true},
		{"5pm is closed", "2019-01-24 17:00", 30, false},
		{"7:30am is before open", "2019-01-24 07:30", 30, false},
		{"quarter past", "2019-01-24 09:15", 30, false},
		{"60 minutes", "2019-01-24 09:00", 60, false},
		{"15 minutes", "2019-01-24 09:00", 15, false},
		{"saturday", "2019-01-26 10:00", 30, false},
		{"sunday", "2019-01-27 10:00", 30, false},
		{"day DST starts (PDT)", "2026-03-09 08:00", 30, true},
		{"day DST ends (PST)", "2026-11-02 16:30", 30, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start := pt(c.start)
			err := ValidateBooking(start, start.Add(time.Duration(c.minutes)*time.Minute), longAgo)
			if (err == nil) != c.ok {
				t.Fatalf("ok=%v, got err=%v", c.ok, err)
			}
			if err != nil && !IsValidation(err) {
				t.Fatalf("want a ValidationError, got %T", err)
			}
		})
	}
}

func TestValidateBookingRejectsPast(t *testing.T) {
	start := pt("2019-01-24 09:00")
	if err := ValidateBooking(start, start.Add(SlotLength), start.Add(time.Minute)); err == nil {
		t.Fatal("expected past booking to be rejected")
	}
}

func TestValidateBookingAcceptsAnyOffset(t *testing.T) {
	// 17:00 UTC on 2019-01-24 is 9:00am PST.
	start := time.Date(2019, 1, 24, 17, 0, 0, 0, time.UTC)
	if err := ValidateBooking(start, start.Add(SlotLength), longAgo); err != nil {
		t.Fatalf("UTC input should be accepted: %v", err)
	}
}

func TestAvailableSlotsFullDay(t *testing.T) {
	slots := AvailableSlots(pt("2019-01-24 00:00"), pt("2019-01-25 00:00"), longAgo, nil)
	if len(slots) != 18 { // 8:00 through 16:30
		t.Fatalf("want 18 slots, got %d", len(slots))
	}
	if !slots[0].Start.Equal(pt("2019-01-24 08:00")) || !slots[17].Start.Equal(pt("2019-01-24 16:30")) {
		t.Fatalf("unexpected bounds: %v .. %v", slots[0].Start, slots[17].Start)
	}
}

func TestAvailableSlotsExcludesBooked(t *testing.T) {
	booked := []Interval{
		{pt("2019-01-24 09:00"), pt("2019-01-24 09:30")},
		{pt("2019-01-24 13:00"), pt("2019-01-24 14:00")}, // a 60-minute legacy row blocks two slots
	}
	slots := AvailableSlots(pt("2019-01-24 00:00"), pt("2019-01-25 00:00"), longAgo, booked)
	if len(slots) != 15 {
		t.Fatalf("want 15 slots, got %d", len(slots))
	}
	for _, s := range slots {
		for _, b := range booked {
			if s.Overlaps(b) {
				t.Fatalf("slot %v overlaps booked %v", s.Start, b.Start)
			}
		}
	}
}

func TestAvailableSlotsWeekendEmpty(t *testing.T) {
	if s := AvailableSlots(pt("2019-01-26 00:00"), pt("2019-01-28 00:00"), longAgo, nil); len(s) != 0 {
		t.Fatalf("weekend should have no slots, got %d", len(s))
	}
}

func TestAvailableSlotsAlignsAndRespectsRangeEnd(t *testing.T) {
	// 9:10 rounds up to 9:30; 10:45 end means the 10:30 slot (ending 11:00) is excluded.
	slots := AvailableSlots(pt("2019-01-24 09:10"), pt("2019-01-24 10:45"), longAgo, nil)
	if len(slots) != 2 || !slots[0].Start.Equal(pt("2019-01-24 09:30")) || !slots[1].Start.Equal(pt("2019-01-24 10:00")) {
		t.Fatalf("unexpected slots: %+v", slots)
	}
}

func TestAvailableSlotsSkipsPast(t *testing.T) {
	now := pt("2019-01-24 12:05")
	slots := AvailableSlots(pt("2019-01-24 00:00"), pt("2019-01-25 00:00"), now, nil)
	if !slots[0].Start.Equal(pt("2019-01-24 12:30")) {
		t.Fatalf("first slot should be 12:30, got %v", slots[0].Start)
	}
}

func TestOverlapsBackToBack(t *testing.T) {
	a := Interval{pt("2019-01-24 09:00"), pt("2019-01-24 09:30")}
	b := Interval{pt("2019-01-24 09:30"), pt("2019-01-24 10:00")}
	if a.Overlaps(b) || b.Overlaps(a) {
		t.Fatal("back-to-back appointments must not overlap")
	}
}

func TestValidateRange(t *testing.T) {
	from := pt("2019-01-01 00:00")
	if ValidateRange(from, from) == nil {
		t.Fatal("empty range should fail")
	}
	if ValidateRange(from, from.Add(32*24*time.Hour)) == nil {
		t.Fatal("over-long range should fail")
	}
	if err := ValidateRange(from, from.Add(7*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
}
