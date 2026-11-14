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
CREATE INDEX memories_rank_key_idx ON memories (rank_key DESC);
CREATE INDEX memories_owner_hash_idx ON memories (owner, content_hash);
CREATE INDEX memories_project_idx ON memories (project);

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

CREATE TABLE memory_links (
    from_id    uuid NOT NULL REFERENCES memories (id) ON DELETE CASCADE,
    to_id      uuid NOT NULL REFERENCES memories (id) ON DELETE CASCADE,
    kind       text NOT NULL CHECK (kind IN ('related', 'supersedes', 'contradicts')),
    owner      text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    removed_at timestamptz,
    PRIMARY KEY (from_id, to_id, kind)
);
CREATE INDEX memory_links_to_idx ON memory_links (to_id);

CREATE TABLE memory_project_access (
    memory_id        uuid NOT NULL REFERENCES memories (id) ON DELETE CASCADE,
    project          text NOT NULL,
    owner            text NOT NULL,
    access_count     bigint NOT NULL DEFAULT 0,
    access_score     double precision NOT NULL DEFAULT 0,
    score_updated_at timestamptz NOT NULL DEFAULT now(),
    rank_key         double precision NOT NULL DEFAULT '-Infinity',
    PRIMARY KEY (memory_id, project)
);
CREATE INDEX memory_project_access_rank_idx ON memory_project_access (project, rank_key DESC);

CREATE TABLE memory_feedback (
    id         uuid PRIMARY KEY,
    memory_id  uuid NOT NULL REFERENCES memories (id) ON DELETE CASCADE,
    owner      text NOT NULL,
    kind       text NOT NULL CHECK (kind IN ('useful', 'wrong', 'outdated')),
    note       text NOT NULL DEFAULT '',
    project    text,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX memory_feedback_memory_idx ON memory_feedback (memory_id);

CREATE TABLE access_events (
    id           uuid PRIMARY KEY,
    owner        text NOT NULL,
    kind         text NOT NULL CHECK (kind IN ('search', 'get')),
    query        text,
    project      text,
    returned_ids uuid[] NOT NULL DEFAULT '{}',
    memory_id    uuid,
    search_id    uuid,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX access_events_created_idx ON access_events (created_at);
CREATE INDEX access_events_memory_idx ON access_events (memory_id);

CREATE TABLE audit_log (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    owner        text NOT NULL,
    action       text NOT NULL,
    memory_id    uuid,
    content_hash bytea,
    cwd          text,
    process      text,
    details      jsonb NOT NULL DEFAULT '{}',
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_log_memory_idx ON audit_log (memory_id);

CREATE TABLE embedding_jobs (
    memory_id       uuid PRIMARY KEY REFERENCES memories (id) ON DELETE CASCADE,
    owner           text NOT NULL,
    target_version  integer NOT NULL,
    attempts        integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    last_error      text,
    done_at         timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX embedding_jobs_pending_idx ON embedding_jobs (next_attempt_at) WHERE done_at IS NULL;

-- +goose Down
DROP TABLE embedding_jobs;
DROP TABLE audit_log;
DROP TABLE access_events;
DROP TABLE memory_feedback;
DROP TABLE memory_project_access;
DROP TABLE memory_links;
DROP TABLE memory_versions;
DROP TABLE memories;
