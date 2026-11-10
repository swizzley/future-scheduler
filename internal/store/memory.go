package store

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/swizzley/future-scheduler/internal/schedule"
)

// Memory is an in-process Store with the same overlap guarantee as Postgres.
// It exists for fast handler tests; the server always runs on Postgres.
type Memory struct {
	mu     sync.Mutex
	nextID int64
	rows   []Appointment
}

// NewMemory returns a Memory store pre-loaded with rows (IDs kept).
func NewMemory(rows ...Appointment) *Memory {
	m := &Memory{nextID: 1}
	for _, r := range rows {
		m.rows = append(m.rows, r)
		if r.ID >= m.nextID {
			m.nextID = r.ID + 1
		}
	}
	return m
}

func (m *Memory) Create(_ context.Context, a Appointment) (Appointment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	want := schedule.Interval{Start: a.StartsAt, End: a.EndsAt}
	for _, r := range m.rows {
		if r.TrainerID == a.TrainerID && want.Overlaps(schedule.Interval{Start: r.StartsAt, End: r.EndsAt}) {
			return Appointment{}, ErrConflict
		}
	}
	a.ID = m.nextID
	m.nextID++
	m.rows = append(m.rows, a)
	return a, nil
}

func (m *Memory) List(_ context.Context, trainerID int64, from, to time.Time) ([]Appointment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	window := schedule.Interval{Start: from, End: to}
	var out []Appointment
	for _, r := range m.rows {
		if r.TrainerID != trainerID {
			continue
		}
		if !from.IsZero() && !to.IsZero() && !window.Overlaps(schedule.Interval{Start: r.StartsAt, End: r.EndsAt}) {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].StartsAt.Equal(out[j].StartsAt) {
			return out[i].StartsAt.Before(out[j].StartsAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// legacyRuleBreaks lists the ways a seed row breaks today's booking rules
// (it ignores "in the past", which every historical row is).
func legacyRuleBreaks(r seedRow) []string {
	var reasons []string
	if d := r.EndedAt.Sub(r.StartedAt); d != schedule.SlotLength {
		reasons = append(reasons, "length is "+d.String()+", not 30m")
	}
	if !schedule.InBusinessHours(r.StartedAt) {
		reasons = append(reasons, "outside Monday-Friday 8am-5pm Pacific")
	}
	return reasons
}
