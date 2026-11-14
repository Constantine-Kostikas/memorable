-- +goose Up
CREATE TABLE memories (
    id               uuid PRIMARY KEY,
    owner            text NOT NULL,
    slug             text,
    project          text,
    type             text NOT NULL CHECK (type IN ('user', 'feedback', 'project', 'reference')),
    title            text NOT NULL,
    content          text NOT NULL,
    tags             text[] NOT NULL DEFAULT '{}',
    source           text NOT NULL CHECK (source IN ('user_stated', 'claude_inferred', 'tool_output', 'imported')),
    status           text NOT NULL CHECK (status IN ('active', 'unverified', 'flagged', 'quarantined', 'superseded', 'deleted')),
    pinned           boolean NOT NULL DEFAULT false,
    expires_at       timestamptz,
    version          integer NOT NULL DEFAULT 1,
    content_hash     bytea NOT NULL,
    fts              tsvector GENERATED ALWAYS AS (
                         setweight(to_tsvector('english', title), 'A') || setweight(to_tsvector('english', content), 'B')
                     ) STORED,
    embedding_model  text,
    embedded_at      timestamptz,
    access_count     bigint NOT NULL DEFAULT 0,
    access_score     double precision NOT NULL DEFAULT 0,
    score_updated_at timestamptz NOT NULL DEFAULT now(),
    rank_key         double precision NOT NULL DEFAULT '-Infinity',
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    last_accessed_at timestamptz,
    deleted_at       timestamptz
);

CREATE UNIQUE INDEX memories_owner_slug_key ON memories (owner, slug) WHERE slug IS NOT NULL AND status <> 'deleted';
CREATE INDEX memories_tags_idx ON memories USING gin (tags);
CREATE INDEX memories_fts_idx ON memories USING gin (fts);
CREATE INDEX memories_title_trgm_idx ON memories USING gin (title gin_trgm_ops);

CREATE TABLE memory_versions (
    memory_id   uuid NOT NULL REFERENCES memories (id) ON DELETE CASCADE,
    version     integer NOT NULL,
    owner       text NOT NULL,
    title       text NOT NULL,
    content     text NOT NULL,
    tags        text[] NOT NULL DEFAULT '{}',
    type        text NOT NULL,
    change_kind text NOT NULL CHECK (change_kind IN ('create', 'update', 'supersede', 'delete', 'restore', 'status')),
    changed_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (memory_id, version)
);

-- +goose Down
DROP TABLE memory_versions;
DROP TABLE memories;
