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

-- pr is the SCM observer's per-session cache of the rich PR facts that do NOT
-- live in the canonical lifecycle (which keeps only pr_state/reason/number/url).
-- 1:1 with a session (a PR is tied to a session by its branch), written by the
-- SCM observer OFF the canonical CDC path (no revision bump, no change_log/outbox
-- event), and cascades away with its session. Scalar facts are typed columns —
-- review_decision/mergeability/ci_state are CHECK-constrained enums and the CI
-- counts are integers, not opaque strings; the list facts (individual checks and
-- review comments) are normalized into pr_check / pr_comment.
CREATE TABLE pr (
    session_id       TEXT PRIMARY KEY REFERENCES sessions (id) ON DELETE CASCADE,
    review_decision  TEXT NOT NULL DEFAULT 'none'
        CHECK (review_decision IN ('none', 'approved', 'changes_requested', 'review_required')),
    mergeability     TEXT NOT NULL DEFAULT 'unknown'
        CHECK (mergeability IN ('unknown', 'mergeable', 'conflicting', 'blocked', 'unstable')),
    ci_state         TEXT NOT NULL DEFAULT 'unknown'
        CHECK (ci_state IN ('unknown', 'pending', 'passing', 'failing')),
    ci_passed        INTEGER NOT NULL DEFAULT 0,
    ci_failed        INTEGER NOT NULL DEFAULT 0,
    ci_pending       INTEGER NOT NULL DEFAULT 0,
    ci_log_tail      TEXT NOT NULL DEFAULT '',
    last_fetched_at  TIMESTAMP NOT NULL
);

-- pr_check is one CI check belonging to a pr (the normalized form of the old
-- ci_summary string). It cascades from pr, so it cannot outlive its PR facts.
CREATE TABLE pr_check (
    session_id  TEXT NOT NULL REFERENCES pr (session_id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'unknown'
        CHECK (status IN ('unknown', 'queued', 'in_progress', 'passed', 'failed', 'skipped', 'cancelled')),
    url         TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (session_id, name)
);

-- pr_comment is one unresolved review comment belonging to a pr (the normalized
-- form of the old pending_comments JSON-in-a-string). Cascades from pr.
CREATE TABLE pr_comment (
    session_id  TEXT NOT NULL REFERENCES pr (session_id) ON DELETE CASCADE,
    comment_id  TEXT NOT NULL,
    author      TEXT NOT NULL DEFAULT '',
    file        TEXT NOT NULL DEFAULT '',
    line        INTEGER NOT NULL DEFAULT 0,
    body        TEXT NOT NULL DEFAULT '',
    resolved    INTEGER NOT NULL DEFAULT 0,
    created_at  TIMESTAMP NOT NULL,
    PRIMARY KEY (session_id, comment_id)
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE pr_comment;
DROP TABLE pr_check;
DROP TABLE pr;
DROP TABLE projects;
-- +goose StatementEnd
