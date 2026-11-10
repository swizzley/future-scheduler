-- btree_gist lets one EXCLUDE constraint combine equality on trainer_id
-- with range overlap on the appointment time.
CREATE EXTENSION IF NOT EXISTS btree_gist;

CREATE TABLE IF NOT EXISTS appointments (
    id          BIGSERIAL   PRIMARY KEY,
    trainer_id  BIGINT      NOT NULL,
    user_id     BIGINT      NOT NULL,
    starts_at   TIMESTAMPTZ NOT NULL,
    ends_at     TIMESTAMPTZ NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT appointments_ends_after_starts CHECK (ends_at > starts_at),
    -- A trainer can never have two overlapping appointments. Ranges are
    -- half-open [start, end), so back-to-back appointments are allowed.
    CONSTRAINT appointments_no_trainer_overlap
        EXCLUDE USING gist (trainer_id WITH =, tstzrange(starts_at, ends_at) WITH &&)
);
