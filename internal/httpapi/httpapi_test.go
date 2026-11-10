package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/swizzley/future-scheduler/internal/schedule"
	"github.com/swizzley/future-scheduler/internal/store"
)

func pt(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, schedule.Pacific)
	if err != nil {
		panic(err)
	}
	return t
}

// newTestServer returns a server whose clock is fixed in January 2019,
// before the seed's appointments, with trainer 1's seed rows for the 24th.
func newTestServer() http.Handler {
	s := &Server{
		Store: store.NewMemory(
			store.Appointment{ID: 1, TrainerID: 1, UserID: 1, StartsAt: pt("2019-01-24 09:00"), EndsAt: pt("2019-01-24 09:30")},
			store.Appointment{ID: 2, TrainerID: 1, UserID: 2, StartsAt: pt("2019-01-24 10:00"), EndsAt: pt("2019-01-24 10:30")},
		),
		Now: func() time.Time { return pt("2019-01-01 00:00") },
	}
	return s.Handler()
}

func do(t *testing.T, h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAvailabilityExcludesBookedSlots(t *testing.T) {
	rec := do(t, newTestServer(), "GET", "/availability?trainer_id=1&starts_at=2019-01-24&ends_at=2019-01-24", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var slots []slotJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &slots); err != nil {
		t.Fatal(err)
	}
	if len(slots) != 16 { // 18 business-hour slots minus 9:00 and 10:00
		t.Fatalf("want 16 slots, got %d", len(slots))
	}
	for _, s := range slots {
		if s.StartsAt.Equal(pt("2019-01-24 09:00")) || s.StartsAt.Equal(pt("2019-01-24 10:00")) {
			t.Fatalf("booked slot %v returned as available", s.StartsAt)
		}
	}
}

func TestAvailabilityAcceptsRFC3339WithUnencodedPlus(t *testing.T) {
	// A "+" offset sent unencoded arrives as a space; it should still parse.
	rec := do(t, newTestServer(), "GET", "/availability?trainer_id=1&starts_at=2019-01-24T16:00:00+00:00&ends_at=2019-01-24T18:00:00+00:00", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

func TestAvailabilityValidation(t *testing.T) {
	h := newTestServer()
	for _, target := range []string{
		"/availability?starts_at=2019-01-24&ends_at=2019-01-24",                // no trainer
		"/availability?trainer_id=abc&starts_at=2019-01-24&ends_at=2019-01-24", // bad trainer
		"/availability?trainer_id=1&ends_at=2019-01-24",                        // no start
		"/availability?trainer_id=1&starts_at=2019-01-24&ends_at=2019-01-23",   // reversed
		"/availability?trainer_id=1&starts_at=2019-01-01&ends_at=2019-03-01",   // too long
		"/availability?trainer_id=1&starts_at=yesterday&ends_at=2019-01-24",    // unparseable
	} {
		if rec := do(t, h, "GET", target, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d", target, rec.Code)
		}
	}
}

func TestCreateAppointment(t *testing.T) {
	h := newTestServer()
	body := `{"trainer_id":1,"user_id":42,"starts_at":"2019-01-24T09:30:00-08:00","ends_at":"2019-01-24T10:00:00-08:00"}`
	rec := do(t, h, "POST", "/appointments", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var got store.Appointment
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != 3 || got.UserID != 42 {
		t.Fatalf("unexpected appointment: %+v", got)
	}

	// The same slot again is a conflict.
	if rec := do(t, h, "POST", "/appointments", body); rec.Code != http.StatusConflict {
		t.Fatalf("double booking: want 409, got %d", rec.Code)
	}
	// And it no longer shows as available.
	rec = do(t, h, "GET", "/availability?trainer_id=1&starts_at=2019-01-24T09:00:00-08:00&ends_at=2019-01-24T11:00:00-08:00", "")
	if strings.Contains(rec.Body.String(), "09:30:00") {
		t.Fatalf("booked slot still listed as available: %s", rec.Body)
	}
}

func TestCreateAppointmentValidation(t *testing.T) {
	h := newTestServer()
	cases := map[string]string{
		"missing fields": `{"trainer_id":1}`,
		"unknown field":  `{"trainer_id":1,"user_id":1,"started_at":"2019-01-24T11:00:00-08:00","ends_at":"2019-01-24T11:30:00-08:00"}`,
		"60 minutes":     `{"trainer_id":1,"user_id":1,"starts_at":"2019-01-24T11:00:00-08:00","ends_at":"2019-01-24T12:00:00-08:00"}`,
		"quarter past":   `{"trainer_id":1,"user_id":1,"starts_at":"2019-01-24T11:15:00-08:00","ends_at":"2019-01-24T11:45:00-08:00"}`,
		"saturday":       `{"trainer_id":1,"user_id":1,"starts_at":"2019-01-26T11:00:00-08:00","ends_at":"2019-01-26T11:30:00-08:00"}`,
		"after hours":    `{"trainer_id":1,"user_id":1,"starts_at":"2019-01-24T17:00:00-08:00","ends_at":"2019-01-24T17:30:00-08:00"}`,
		"bad json":       `{"trainer_id":`,
		"zero trainer":   `{"trainer_id":0,"user_id":1,"starts_at":"2019-01-24T11:00:00-08:00","ends_at":"2019-01-24T11:30:00-08:00"}`,
	}
	for name, body := range cases {
		if rec := do(t, h, "POST", "/appointments", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d (%s)", name, rec.Code, rec.Body)
		}
	}
}

func TestListAppointments(t *testing.T) {
	rec := do(t, newTestServer(), "GET", "/appointments?trainer_id=1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var got []store.Appointment
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 2 {
		t.Fatalf("unexpected list: %+v", got)
	}
	if !strings.Contains(rec.Body.String(), `"starts_at":"2019-01-24T09:00:00-08:00"`) {
		t.Fatalf("times should be returned in Pacific: %s", rec.Body)
	}
}

func TestListAppointmentsRequiresTrainer(t *testing.T) {
	if rec := do(t, newTestServer(), "GET", "/appointments", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestListUnknownTrainerIsEmptyArray(t *testing.T) {
	rec := do(t, newTestServer(), "GET", "/appointments?trainer_id=99", "")
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("want [], got %s", rec.Body)
	}
}
