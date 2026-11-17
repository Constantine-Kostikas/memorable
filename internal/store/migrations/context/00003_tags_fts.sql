-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION memorable_tags_text(text[]) RETURNS text
    LANGUAGE sql IMMUTABLE PARALLEL SAFE
    AS $$ SELECT array_to_string($1, ' ') $$;
-- +goose StatementEnd

DROP INDEX memories_fts_idx;
ALTER TABLE memories DROP COLUMN fts;
ALTER TABLE memories ADD COLUMN fts tsvector GENERATED ALWAYS AS (
    setweight(to_tsvector('english', title), 'A')
    || setweight(to_tsvector('english', memorable_tags_text(tags)), 'A')
    || setweight(to_tsvector('english', content), 'B')
) STORED;
CREATE INDEX memories_fts_idx ON memories USING gin (fts);

-- +goose Down
DROP INDEX memories_fts_idx;
ALTER TABLE memories DROP COLUMN fts;
ALTER TABLE memories ADD COLUMN fts tsvector GENERATED ALWAYS AS (
    setweight(to_tsvector('english', title), 'A') || setweight(to_tsvector('english', content), 'B')
) STORED;
CREATE INDEX memories_fts_idx ON memories USING gin (fts);
DROP FUNCTION memorable_tags_text(text[]);
