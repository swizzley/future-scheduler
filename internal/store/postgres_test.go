package store

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/swizzley/future-scheduler/internal/schedule"
)

// These tests need a real Postgres: set TEST_DATABASE_URL (make test-integration
// starts one with docker compose). They are skipped otherwise.
func testDB(t *testing.T) *Postgres {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping Postgres integration test")
	}
	ctx := context.Background()
	db, err := NewPostgres(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(ctx, `TRUNCATE appointments RESTART IDENTITY`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db
}

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, schedule.Pacific)
	if err != nil {
		panic(err)
	}
	return t
}

func TestPostgresSeed(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	var warned []int64
	n, err := db.Seed(ctx, "../../appointments.json", func(id int64, _ string) { warned = append(warned, id) })
	if err != nil {
		t.Fatal(err)
	}
	if n != 10 {
		t.Fatalf("want 10 seeded rows, got %d", n)
	}
	// Rows 5, 7-10 are on Saturday 2019-01-26; row 9 is also 60 minutes.
	if len(warned) != 6 {
		t.Fatalf("want 6 rule warnings, got %v", warned)
	}
	// Seeding twice is a no-op.
	if n, err := db.Seed(ctx, "../../appointments.json", nil); err != nil || n != 0 {
		t.Fatalf("reseed: n=%d err=%v", n, err)
	}
	// New IDs continue after the seed.
	a, err := db.Create(ctx, Appointment{TrainerID: 1, UserID: 1, StartsAt: at("2019-01-28 09:00"), EndsAt: at("2019-01-28 09:30")})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != 11 {
		t.Fatalf("want id 11 after seed, got %d", a.ID)
	}
	// The 60-minute legacy row blocks a booking in its second half.
	_, err = db.Create(ctx, Appointment{TrainerID: 3, UserID: 1, StartsAt: at("2019-01-26 13:30"), EndsAt: at("2019-01-26 14:00")})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
}

func TestPostgresOverlapAndBackToBack(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	mk := func(trainer int64, start string) error {
		s := at(start)
		_, err := db.Create(ctx, Appointment{TrainerID: trainer, UserID: 1, StartsAt: s, EndsAt: s.Add(schedule.SlotLength)})
		return err
	}
	if err := mk(1, "2019-01-28 09:00"); err != nil {
		t.Fatal(err)
	}
	if err := mk(1, "2019-01-28 09:00"); !errors.Is(err, ErrConflict) {
		t.Fatalf("same slot: want ErrConflict, got %v", err)
	}
	if err := mk(1, "2019-01-28 09:30"); err != nil {
		t.Fatalf("back-to-back should be allowed: %v", err)
	}
	if err := mk(2, "2019-01-28 09:00"); err != nil {
		t.Fatalf("a different trainer at the same time should be allowed: %v", err)
	}
}

// TestPostgresConcurrentBooking races many requests for one slot: the
// database constraint must let exactly one win.
func TestPostgresConcurrentBooking(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	start := at("2019-01-28 11:00")

	const n = 25
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		wins      int
		conflicts int
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(user int64) {
			defer wg.Done()
			_, err := db.Create(ctx, Appointment{TrainerID: 1, UserID: user, StartsAt: start, EndsAt: start.Add(schedule.SlotLength)})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case errors.Is(err, ErrConflict):
				conflicts++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}(int64(i + 1))
	}
	wg.Wait()
	if wins != 1 || conflicts != n-1 {
		t.Fatalf("want 1 win and %d conflicts, got %d wins and %d conflicts", n-1, wins, conflicts)
	}
}

func TestPostgresListWindow(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if _, err := db.Seed(ctx, "../../appointments.json", nil); err != nil {
		t.Fatal(err)
	}
	all, err := db.List(ctx, 1, time.Time{}, time.Time{})
	if err != nil || len(all) != 5 {
		t.Fatalf("trainer 1: want 5, got %d (%v)", len(all), err)
	}
	day, err := db.List(ctx, 1, at("2019-01-25 00:00"), at("2019-01-26 00:00"))
	if err != nil || len(day) != 2 {
		t.Fatalf("trainer 1 on the 25th: want 2, got %d (%v)", len(day), err)
	}
}
