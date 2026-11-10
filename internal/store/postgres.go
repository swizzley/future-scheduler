package store

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schema string

// exclusionViolation is the Postgres error code raised by the EXCLUDE
// constraint that forbids overlapping appointments for one trainer.
const exclusionViolation = "23P01"

// Postgres is the production Store.
type Postgres struct {
	pool *pgxpool.Pool
}

// NewPostgres connects and applies the schema (idempotent).
func NewPostgres(ctx context.Context, url string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	if _, err := pool.Exec(ctx, schema); err != nil {
		pool.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Postgres{pool: pool}, nil
}

// Close releases the connection pool.
func (p *Postgres) Close() { p.pool.Close() }

// Ping checks the database is reachable (used by /healthz).
func (p *Postgres) Ping(ctx context.Context) error { return p.pool.Ping(ctx) }

// Create inserts an appointment. Overlap is enforced by the database itself,
// so two concurrent requests for the same slot can't both succeed.
func (p *Postgres) Create(ctx context.Context, a Appointment) (Appointment, error) {
	err := p.pool.QueryRow(ctx,
		`INSERT INTO appointments (trainer_id, user_id, starts_at, ends_at)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		a.TrainerID, a.UserID, a.StartsAt, a.EndsAt,
	).Scan(&a.ID)
	if isExclusionViolation(err) {
		return Appointment{}, ErrConflict
	}
	if err != nil {
		return Appointment{}, fmt.Errorf("insert appointment: %w", err)
	}
	return a, nil
}

// List returns a trainer's appointments in start order, optionally limited to [from, to).
func (p *Postgres) List(ctx context.Context, trainerID int64, from, to time.Time) ([]Appointment, error) {
	q := `SELECT id, trainer_id, user_id, starts_at, ends_at FROM appointments WHERE trainer_id = $1`
	args := []any{trainerID}
	if !from.IsZero() && !to.IsZero() {
		q += ` AND tstzrange(starts_at, ends_at) && tstzrange($2, $3)`
		args = append(args, from, to)
	}
	q += ` ORDER BY starts_at, id`

	rows, err := p.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list appointments: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Appointment, error) {
		var a Appointment
		err := r.Scan(&a.ID, &a.TrainerID, &a.UserID, &a.StartsAt, &a.EndsAt)
		return a, err
	})
	if err != nil {
		return nil, fmt.Errorf("scan appointments: %w", err)
	}
	return out, nil
}

// seedRow matches appointments.json, which names its fields started_at /
// ended_at (the API uses starts_at / ends_at, as the assignment specifies).
type seedRow struct {
	ID        int64     `json:"id"`
	TrainerID int64     `json:"trainer_id"`
	UserID    int64     `json:"user_id"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
}

// Seed loads existing appointments from a JSON file. It keeps the original
// IDs, is safe to run on every start (rows already present are skipped), and
// imports rows as-is even when they break today's booking rules: they are
// historical records, and they still block the trainer's time. Rule-breaking
// rows are logged so they are visible, not silently "fixed".
func (p *Postgres) Seed(ctx context.Context, path string, warn func(seedID int64, reason string)) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read seed: %w", err)
	}
	var rows []seedRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return 0, fmt.Errorf("parse seed: %w", err)
	}

	inserted := 0
	for _, r := range rows {
		if warn != nil {
			for _, reason := range legacyRuleBreaks(r) {
				warn(r.ID, reason)
			}
		}
		tag, err := p.pool.Exec(ctx,
			`INSERT INTO appointments (id, trainer_id, user_id, starts_at, ends_at)
			 VALUES ($1, $2, $3, $4, $5) ON CONFLICT (id) DO NOTHING`,
			r.ID, r.TrainerID, r.UserID, r.StartedAt, r.EndedAt)
		if isExclusionViolation(err) {
			slog.Warn("seed row overlaps an existing appointment; skipped", "seed_id", r.ID)
			continue
		}
		if err != nil {
			return inserted, fmt.Errorf("insert seed row %d: %w", r.ID, err)
		}
		inserted += int(tag.RowsAffected())
	}

	// Keep the ID sequence ahead of the seeded IDs.
	if _, err := p.pool.Exec(ctx,
		`SELECT setval(pg_get_serial_sequence('appointments', 'id'),
		               GREATEST((SELECT COALESCE(MAX(id), 0) FROM appointments), 1))`); err != nil {
		return inserted, fmt.Errorf("reset id sequence: %w", err)
	}
	return inserted, nil
}

func isExclusionViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == exclusionViolation
}
