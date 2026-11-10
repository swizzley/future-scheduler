// Package httpapi is the JSON HTTP layer. Endpoint shapes follow the
// assignment literally: query parameters and the field names trainer_id,
// user_id, starts_at and ends_at.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/swizzley/future-scheduler/internal/schedule"
	"github.com/swizzley/future-scheduler/internal/store"
)

// Server wires the store and a clock into HTTP handlers.
type Server struct {
	Store store.Store
	Now   func() time.Time            // injectable so tests are deterministic
	Ping  func(context.Context) error // optional database health check
}

// Handler returns the routed http.Handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /availability", s.availability)
	mux.HandleFunc("POST /appointments", s.createAppointment)
	mux.HandleFunc("GET /appointments", s.listAppointments)
	mux.HandleFunc("GET /healthz", s.healthz)
	return mux
}

// slotJSON is one available time.
type slotJSON struct {
	StartsAt time.Time `json:"starts_at"`
	EndsAt   time.Time `json:"ends_at"`
}

// GET /availability?trainer_id=1&starts_at=...&ends_at=...
func (s *Server) availability(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	trainerID, err := requiredID(q.Get("trainer_id"), "trainer_id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	from, err := parseTimeParam(q.Get("starts_at"), "starts_at", false)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	to, err := parseTimeParam(q.Get("ends_at"), "ends_at", true)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := schedule.ValidateRange(from, to); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	booked, err := s.Store.List(r.Context(), trainerID, from, to)
	if err != nil {
		s.internalError(w, err)
		return
	}
	intervals := make([]schedule.Interval, len(booked))
	for i, b := range booked {
		intervals[i] = schedule.Interval{Start: b.StartsAt, End: b.EndsAt}
	}

	slots := schedule.AvailableSlots(from, to, s.Now(), intervals)
	out := make([]slotJSON, len(slots))
	for i, sl := range slots {
		out[i] = slotJSON{StartsAt: sl.Start.In(schedule.Pacific), EndsAt: sl.End.In(schedule.Pacific)}
	}
	writeJSON(w, http.StatusOK, out)
}

// createRequest uses pointers so a missing field is distinguishable from a zero value.
type createRequest struct {
	TrainerID *int64     `json:"trainer_id"`
	UserID    *int64     `json:"user_id"`
	StartsAt  *time.Time `json:"starts_at"`
	EndsAt    *time.Time `json:"ends_at"`
}

// POST /appointments
func (s *Server) createAppointment(w http.ResponseWriter, r *http.Request) {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields() // catches typos such as started_at
	var req createRequest
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	var missing []string
	if req.TrainerID == nil {
		missing = append(missing, "trainer_id")
	}
	if req.UserID == nil {
		missing = append(missing, "user_id")
	}
	if req.StartsAt == nil {
		missing = append(missing, "starts_at")
	}
	if req.EndsAt == nil {
		missing = append(missing, "ends_at")
	}
	if len(missing) > 0 {
		writeError(w, http.StatusBadRequest, "missing required fields: "+strings.Join(missing, ", "))
		return
	}
	if *req.TrainerID <= 0 || *req.UserID <= 0 {
		writeError(w, http.StatusBadRequest, "trainer_id and user_id must be positive integers")
		return
	}
	if err := schedule.ValidateBooking(*req.StartsAt, *req.EndsAt, s.Now()); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	created, err := s.Store.Create(r.Context(), store.Appointment{
		TrainerID: *req.TrainerID,
		UserID:    *req.UserID,
		StartsAt:  *req.StartsAt,
		EndsAt:    *req.EndsAt,
	})
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toPacific(created))
}

// GET /appointments?trainer_id=1 (optional starts_at / ends_at filter)
func (s *Server) listAppointments(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	trainerID, err := requiredID(q.Get("trainer_id"), "trainer_id")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var from, to time.Time
	if q.Get("starts_at") != "" || q.Get("ends_at") != "" {
		if from, err = parseTimeParam(q.Get("starts_at"), "starts_at", false); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if to, err = parseTimeParam(q.Get("ends_at"), "ends_at", true); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if !to.After(from) {
			writeError(w, http.StatusBadRequest, "ends_at must be after starts_at")
			return
		}
	}

	appts, err := s.Store.List(r.Context(), trainerID, from, to)
	if err != nil {
		s.internalError(w, err)
		return
	}
	out := make([]store.Appointment, len(appts))
	for i, a := range appts {
		out[i] = toPacific(a)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	if s.Ping != nil {
		if err := s.Ping(r.Context()); err != nil {
			writeError(w, http.StatusServiceUnavailable, "database unavailable")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) internalError(w http.ResponseWriter, err error) {
	slog.Error("request failed", "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

func requiredID(raw, name string) (int64, error) {
	if raw == "" {
		return 0, fmt.Errorf("%s is required", name)
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return id, nil
}

// parseTimeParam accepts RFC 3339 (2019-01-24T09:00:00-08:00) or a bare date
// (2019-01-24), which is read as a Pacific calendar day. A bare-date end is
// inclusive: ends_at=2019-01-25 covers all of the 25th.
func parseTimeParam(raw, name string, isEnd bool) (time.Time, error) {
	if raw == "" {
		return time.Time{}, fmt.Errorf("%s is required", name)
	}
	// An unencoded "+" in a query string arrives as a space.
	raw = strings.ReplaceAll(raw, " ", "+")
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, nil
	}
	if d, err := time.ParseInLocation(time.DateOnly, raw, schedule.Pacific); err == nil {
		if isEnd {
			d = d.AddDate(0, 0, 1)
		}
		return d, nil
	}
	return time.Time{}, fmt.Errorf("%s must be RFC 3339 (2019-01-24T09:00:00-08:00) or a date (2019-01-24)", name)
}

func toPacific(a store.Appointment) store.Appointment {
	a.StartsAt = a.StartsAt.In(schedule.Pacific)
	a.EndsAt = a.EndsAt.In(schedule.Pacific)
	return a
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("encode response", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
