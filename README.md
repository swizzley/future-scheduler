# Appointment Scheduling API

A Go HTTP JSON API that lets a client book a 30-minute video call with their trainer. It is backed by Postgres, which enforces that a trainer's appointments never overlap.

## Run it

```bash
docker compose up --build
```

This starts Postgres, applies the schema, loads `appointments.json`, and serves the API on `http://localhost:8080`. Stop and reset with `docker compose down -v`.

Tests:

```bash
make test               # unit tests, no database
make test-integration   # adds the Postgres tests (starts Postgres in Docker), with -race
```

## Endpoints

The shapes follow the assignment literally: query parameters, and the field names `trainer_id`, `user_id`, `starts_at`, `ends_at`. Times are RFC 3339 with any offset; responses are returned in Pacific time. Errors are `{"error": "..."}`.

### `GET /availability?trainer_id=&starts_at=&ends_at=`

Returns the open 30-minute slots in the range: those inside business hours, not in the past, and not overlapping an existing appointment.

`starts_at` / `ends_at` may also be plain dates, read as Pacific calendar days, and the end date is inclusive (`starts_at=2026-11-16&ends_at=2026-11-20` is that whole week). The range is capped at 31 days.

```bash
curl 'localhost:8080/availability?trainer_id=1&starts_at=2026-11-16&ends_at=2026-11-16'
```
```json
[{"starts_at":"2026-11-16T08:00:00-08:00","ends_at":"2026-11-16T08:30:00-08:00"}, ...]
```

### `POST /appointments`

```bash
curl -X POST localhost:8080/appointments -d '{
  "trainer_id": 1, "user_id": 42,
  "starts_at": "2026-11-16T09:00:00-08:00",
  "ends_at":   "2026-11-16T09:30:00-08:00"
}'
```

- `201` returns the created appointment.
- `400` is returned when:
  - a field is missing
  - a field name is unknown, which catches typos such as `started_at`
  - the length isn't exactly 30 minutes
  - the start isn't on :00 or :30
  - the time is outside Monday–Friday, 8am–5pm Pacific
  - the time is in the past
- `409` means the trainer already has an appointment at that time.

### `GET /appointments?trainer_id=`

Lists a trainer's appointments in start order. It also accepts an optional `starts_at` / `ends_at` window.

```bash
curl 'localhost:8080/appointments?trainer_id=1'
```

`GET /healthz` checks the database connection.

## Design decisions

- **Overlap is prevented by the database, not just the code.** An `EXCLUDE USING gist (trainer_id WITH =, tstzrange(starts_at, ends_at) WITH &&)` constraint makes double-booking impossible even when two requests race for the same slot. The API maps the constraint violation (`23P01`) to `409`. `TestPostgresConcurrentBooking` fires 25 simultaneous bookings at one slot and checks that exactly one wins. Ranges are half-open, so back-to-back appointments are fine.
- **Business rules are evaluated in `America/Los_Angeles` wall-clock time.** "8am–5pm Pacific" stays correct across daylight saving changes. Storage is `timestamptz`.
- **The rules are pure functions** in `internal/schedule`, with "now" injected. That keeps them easy to test and to change. The HTTP layer (`internal/httpapi`) and storage (`internal/store`) are thin around them.
- **Dependencies are the standard library's `net/http` routing and `pgx`.** There's no framework or ORM.

## Notes on `appointments.json`

The seed data doesn't all follow the booking rules:

- 2019-01-26 is a **Saturday**, and rows 5, 7, 8, 9 and 10 fall on it.
- Row 9 is **60 minutes** long (13:00–14:00).
- The file uses `started_at` / `ended_at`, while the API uses `starts_at` / `ends_at` as specified.

I import these rows **as they are**, because they are existing records and the system shouldn't rewrite or drop data it was given. They still **block** the trainer's time, so the 60-minute row blocks two slots. The rules apply to new bookings. Each rule-breaking row is logged at startup so it's visible. Seeding keeps the original IDs, is safe to rerun, and moves the ID sequence past them.

All seed rows are in 2019. Availability never offers past slots, so use current dates to try the endpoints. The tests pin the clock to 2019 to exercise the seed.

## What I'd add next

- Cancel and reschedule (`DELETE` / `PATCH /appointments/{id}`). The overlap constraint keeps holding.
- Prevent a client from double-booking themselves across trainers.
- Per-trainer working hours and time zones instead of one global window.
- Variable appointment lengths. The constraint already supports them; the slot generator needs a length parameter.
- An idempotency key on `POST`, and pagination on the appointment list.
- Authentication, so a client can only book as themselves.

## How I used AI

The take-home invited it, so here it is. I wrote a short spec first: the endpoints, the seed-data handling, time-zone rules, and enforcing overlap in Postgres. I then used Claude Code to implement and test it against that spec, and I reviewed the code and tests.
