// Package store persists appointments.
package store

import (
	"context"
	"errors"
	"time"
)

// ErrConflict means the trainer already has an appointment overlapping the requested time.
var ErrConflict = errors.New("trainer already has an appointment at that time")

// Appointment is one booked call between a trainer and a client.
type Appointment struct {
	ID        int64     `json:"id"`
	TrainerID int64     `json:"trainer_id"`
	UserID    int64     `json:"user_id"`
	StartsAt  time.Time `json:"starts_at"`
	EndsAt    time.Time `json:"ends_at"`
}

// Store is what the HTTP layer needs. Postgres is the real implementation;
// Memory backs fast handler tests.
type Store interface {
	// Create inserts a new appointment and returns it with its ID set.
	// It returns ErrConflict if the trainer is already booked at that time.
	Create(ctx context.Context, a Appointment) (Appointment, error)
	// List returns a trainer's appointments ordered by start time. If from and
	// to are non-zero, only appointments overlapping [from, to) are returned.
	List(ctx context.Context, trainerID int64, from, to time.Time) ([]Appointment, error)
}
