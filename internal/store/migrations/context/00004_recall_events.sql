-- +goose Up
ALTER TABLE access_events DROP CONSTRAINT access_events_kind_check;
ALTER TABLE access_events ADD CONSTRAINT access_events_kind_check CHECK (kind IN ('search', 'get', 'inject', 'preload'));
ALTER TABLE access_events ADD COLUMN session_id text;
ALTER TABLE access_events ADD COLUMN details jsonb NOT NULL DEFAULT '{}';
CREATE INDEX access_events_session_idx ON access_events (session_id, kind) WHERE session_id IS NOT NULL;
CREATE INDEX access_events_search_idx ON access_events (search_id) WHERE search_id IS NOT NULL;

CREATE TABLE calibrations (
    id         uuid PRIMARY KEY,
    owner      text NOT NULL,
    model      text NOT NULL,
    dimensions int NOT NULL,
    b0         float8 NOT NULL,
    b1         float8 NOT NULL,
    b2         float8 NOT NULL,
    b3         float8 NOT NULL,
    b4         float8 NOT NULL,
    positives  int NOT NULL,
    negatives  int NOT NULL,
    source     text NOT NULL CHECK (source IN ('default', 'usage')),
    fitted_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX calibrations_model_idx ON calibrations (owner, model, dimensions, fitted_at DESC);

-- +goose Down
DROP TABLE calibrations;
DROP INDEX access_events_search_idx;
DROP INDEX access_events_session_idx;
DELETE FROM access_events WHERE kind IN ('inject', 'preload');
ALTER TABLE access_events DROP COLUMN details;
ALTER TABLE access_events DROP COLUMN session_id;
ALTER TABLE access_events DROP CONSTRAINT access_events_kind_check;
ALTER TABLE access_events ADD CONSTRAINT access_events_kind_check CHECK (kind IN ('search', 'get'));
