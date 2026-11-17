-- The profile schema has no access events; this keeps its version in step with context schemas.
-- +goose Up
SELECT 1;

-- +goose Down
SELECT 1;
