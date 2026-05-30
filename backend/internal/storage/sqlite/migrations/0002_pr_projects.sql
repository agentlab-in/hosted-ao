-- +goose Up
-- +goose StatementBegin

-- projects is the durable registry of repos AO manages, the SQLite twin of the
-- old YAML config (global config.yaml + per-repo agent-orchestrator.yaml). id is
-- the {basename}_{sha256(path:originUrl)[:10]} key the session layer references
-- via sessions.project_id. The relationship is app-enforced, NOT a hard FK:
-- SQLite cannot ALTER ADD a FK without a table rebuild, and an existing-session
-- backfill may land sessions before their project row.
CREATE TABLE projects (
    id               TEXT PRIMARY KEY,
    path             TEXT NOT NULL,
    repo_owner       TEXT NOT NULL DEFAULT '',
    repo_name        TEXT NOT NULL DEFAULT '',
    repo_platform    TEXT NOT NULL DEFAULT '',
    repo_origin_url  TEXT NOT NULL DEFAULT '',
    default_branch   TEXT NOT NULL DEFAULT '',
    display_name     TEXT NOT NULL DEFAULT '',
    session_prefix   TEXT NOT NULL DEFAULT '',
    source           TEXT NOT NULL DEFAULT '',
    registered_at    TIMESTAMP NOT NULL,

    -- soft delete: NULL = active. Archiving keeps the row so a session's
    -- project_id always resolves (there is no FK to enforce it), avoiding
    -- dangling references; active-only reads filter archived_at IS NULL.
    archived_at      TIMESTAMP
);

-- pr_enrichment is the SCM observer's per-session cache of the rich PR facts that
-- do NOT live in the canonical lifecycle (which keeps only pr_state/reason/number/
-- url). It is 1:1 with a session (a PR is always tied to a session by its branch),
-- written by the SCM observer OFF the canonical CDC path (no revision bump, no
-- change_log/outbox event), and cascades away with its session.
CREATE TABLE pr_enrichment (
    session_id       TEXT PRIMARY KEY REFERENCES sessions (id) ON DELETE CASCADE,
    ci_summary       TEXT NOT NULL DEFAULT '',
    review_decision  TEXT NOT NULL DEFAULT '',
    mergeability     TEXT NOT NULL DEFAULT '',
    pending_comments TEXT NOT NULL DEFAULT '',
    ci_log_tail      TEXT NOT NULL DEFAULT '',
    last_fetched_at  TIMESTAMP NOT NULL
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE pr_enrichment;
DROP TABLE projects;
-- +goose StatementEnd
